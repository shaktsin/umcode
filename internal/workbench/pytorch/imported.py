"""Offline, one-request runner for locally copied Hugging Face checkpoints."""
import base64
import io
import json
import math
import os
import sys
from pathlib import Path

os.environ['HF_HUB_OFFLINE'] = '1'
os.environ['TRANSFORMERS_OFFLINE'] = '1'
os.environ['TOKENIZERS_PARALLELISM'] = 'false'

MAX_REQUEST = 64 * 1024 * 1024
MAX_IMAGE = 8 * 1024 * 1024
MOE_TYPES = {'mixtral', 'granitemoe', 'qwen2_moe', 'qwen3_moe'}
TASK_TYPES = {'text-generation': {'gpt2', 'llama', 'mistral', 'qwen2'} | MOE_TYPES,
              'text-classification': {'bert'},
              'image-classification': {'vit', 'resnet', 'convnext'}}


def check_config_budget(config):
    limits = {'num_hidden_layers': 128, 'n_layer': 128, 'hidden_size': 8192,
              'n_embd': 8192, 'intermediate_size': 65536, 'n_inner': 65536,
              'num_attention_heads': 128, 'n_head': 128, 'vocab_size': 256000,
              'num_labels': 10000, 'image_size': 512, 'embedding_size': 4096,
              'max_position_embeddings': 131072, 'n_positions': 131072,
              'num_local_experts': 512, 'num_experts': 512, 'num_experts_per_tok': 512,
              'moe_intermediate_size': 65536, 'shared_expert_intermediate_size': 65536}
    for field, limit in limits.items():
        value = getattr(config, field, None)
        if value is not None:
            values = value if isinstance(value, (list, tuple)) else [value]
            if any(isinstance(v, bool) or not isinstance(v, int) or v < 1 or v > limit for v in values):
                raise ValueError('Model config exceeds the local runtime budget: ' + field)
    if config.model_type in MOE_TYPES:
        experts = getattr(config, 'num_local_experts', getattr(config, 'num_experts', 0))
        if not 1 <= config.num_experts_per_tok <= experts:
            raise ValueError('MoE experts per token must be between one and the total expert count')
    for field, limit in [('depths', 48), ('hidden_sizes', 4096)]:
        values = getattr(config, field, None)
        if values is not None and (not isinstance(values, (list, tuple)) or not 1 <= len(values) <= 8 or
                                   any(not isinstance(v, int) or not 1 <= v <= limit for v in values)):
            raise ValueError('Model config exceeds the local runtime budget: ' + field)
    if config.model_type == 'vit':
        side, patch = config.image_size, config.patch_size
        if not isinstance(side, int) or not isinstance(patch, int) or patch < 1 or (side // patch)**2 > 1024:
            raise ValueError('Vision token count exceeds the local runtime budget')


def check_processor_budget(settings):
    for field in ('size', 'crop_size'):
        values = settings.get(field, {})
        values = list(values.values()) if isinstance(values, dict) else values if isinstance(values, (list, tuple)) else [values]
        if any(isinstance(v, bool) or not isinstance(v, int) or not 1 <= v <= 512 for v in values):
            raise ValueError('Image preprocessing exceeds the local pixel budget')
    crop = settings.get('crop_pct', 1.0)
    if not isinstance(crop, (int, float)) or not math.isfinite(crop) or not .5 <= crop <= 1:
        raise ValueError('Image crop exceeds the local pixel budget')


def number(value, default, low, high, integer=False):
    if value is None:
        return default
    if isinstance(value, bool) or not isinstance(value, (int, float)) or not math.isfinite(value):
        raise ValueError('Invalid numeric option')
    if value < low or value > high or (integer and int(value) != value):
        raise ValueError('Numeric option is outside supported bounds')
    return int(value) if integer else float(value)


def load(request):
    from transformers import (AutoConfig, AutoModelForCausalLM, AutoModelForSequenceClassification,
                              AutoModelForImageClassification, AutoTokenizer, AutoImageProcessor)
    task = request.get('task')
    if task not in TASK_TYPES:
        raise ValueError('Unsupported task')
    raw = request.get('checkpointDir') or request.get('bundleDir')
    if not isinstance(raw, str) or not raw or len(raw) > 4096:
        raise ValueError('A local bundle directory is required')
    path = Path(raw).resolve(strict=True)
    if not path.is_dir() or not (path / 'config.json').is_file() or not list(path.glob('*.safetensors')):
        raise ValueError('Bundle must include config.json and safetensors weights')
    options = dict(local_files_only=True, trust_remote_code=False)
    config = AutoConfig.from_pretrained(str(path), **options)
    if config.model_type not in TASK_TYPES[task] or getattr(config, 'quantization_config', None):
        raise ValueError('Unsupported model architecture or quantization')
    check_config_budget(config)
    cls = {'text-generation': AutoModelForCausalLM, 'text-classification': AutoModelForSequenceClassification,
           'image-classification': AutoModelForImageClassification}[task]
    # Meta tensors have shapes but allocate no weight storage. Check the real
    # architecture size and header coverage before loading/initializing tensors.
    import torch
    from safetensors import safe_open
    with torch.device('meta'):
        skeleton = cls.from_config(config, trust_remote_code=False)
    if sum(p.numel() for p in skeleton.parameters()) > 1_000_000_000:
        raise ValueError('Model exceeds the local one-billion-total-parameter budget. All MoE experts must fit in memory, not only active experts. Use a smaller model; Build inspection remains available.')
    expected = {key: tuple(value.shape) for key, value in skeleton.state_dict().items()}
    present = {}
    for file in path.glob('*.safetensors'):
        with safe_open(file, framework='pt', device='cpu') as tensors:
            for key in tensors.keys():
                if key in present:
                    raise ValueError('Duplicate weight tensor')
                present[key] = tuple(tensors.get_slice(key).get_shape())
    if task == 'text-generation' and getattr(config, 'tie_word_embeddings', False) and 'lm_head.weight' not in present:
        tied = 'transformer.wte.weight' if config.model_type == 'gpt2' else 'model.embed_tokens.weight'
        if present.get(tied) == expected.get('lm_head.weight'):
            expected.pop('lm_head.weight', None)
    # Older GPT-2 exports persist causal masks that modern Transformers
    # regenerates as non-persistent buffers. Never ignore learned weights or
    # arbitrary extra keys; accept only known buffers with their exact shape.
    if config.model_type == 'gpt2':
        buffers = {key: tuple(value.shape) for key, value in skeleton.named_buffers()}
        for layer in range(config.n_layer):
            for suffix in ('bias', 'masked_bias'):
                key = f'transformer.h.{layer}.attn.{suffix}'
                if key not in expected and key in buffers and present.get(key) == buffers[key]:
                    present.pop(key)
    if expected != present:
        raise ValueError('Checkpoint weights do not match the selected model architecture')
    del skeleton
    model, info = cls.from_pretrained(str(path), use_safetensors=True, output_loading_info=True, **options)
    if any(info.get(k) for k in ('missing_keys', 'unexpected_keys', 'mismatched_keys', 'error_msgs')):
        raise ValueError('Checkpoint weights do not match the selected model architecture')
    if task == 'image-classification':
        settings = json.loads((path / 'preprocessor_config.json').read_text(encoding='utf-8'))
        check_processor_budget(settings)
        processor = AutoImageProcessor.from_pretrained(str(path), use_fast=False, **options)
        check_processor_budget(processor.to_dict())
    else:
        processor = AutoTokenizer.from_pretrained(str(path), **options)
        if processor.pad_token_id is None:
            if processor.eos_token_id is None:
                raise ValueError('Tokenizer requires a pad or EOS token')
            processor.pad_token = processor.eos_token
    model.eval()
    return path, task, model, processor


def image_from_base64(value):
    from PIL import Image
    if not isinstance(value, str) or len(value) > MAX_IMAGE * 2:
        raise ValueError('Image is missing or too large')
    try:
        data = base64.b64decode(value, validate=True)
        if len(data) > MAX_IMAGE:
            raise ValueError('Image is too large')
        image = Image.open(io.BytesIO(data))
        if image.width * image.height > 16_000_000 or max(image.size) > min(image.size) * 8:
            raise ValueError('Image dimensions are too large')
        return image.convert('RGB')
    except (ValueError, OSError) as exc:
        raise ValueError('Invalid image data') from exc


def text_value(value):
    if not isinstance(value, str) or not value.strip() or len(value) > 100_000:
        raise ValueError('Text is missing or too large')
    return value


def target_index(value, model):
    if isinstance(value, str):
        labels = [str(model.config.id2label.get(i, i)) for i in range(model.config.num_labels)]
        if labels.count(value) != 1:
            raise ValueError('Classification target must match exactly one pretrained label')
        return labels.index(value)
    if isinstance(value, bool) or not isinstance(value, int) or not 0 <= value < model.config.num_labels:
        raise ValueError('Classification target is outside the pretrained label space')
    return value


def chat_inputs(processor, messages, capacity, maximum):
    if not getattr(processor, 'chat_template', None):
        raise ValueError('This tokenizer has no chat template; use completion inference instead')
    if not isinstance(messages, list) or not 1 <= len(messages) <= 64:
        raise ValueError('Chat requires 1 to 64 messages')
    for index, message in enumerate(messages):
        role = 'user' if index % 2 == 0 else 'assistant'
        if not isinstance(message, dict) or message.get('role') != role:
            raise ValueError('Chat roles must alternate user and assistant')
        text_value(message.get('content'))
    if messages[-1]['role'] != 'user':
        raise ValueError('The last chat role must be user')
    prompt = processor.apply_chat_template(messages, tokenize=False, add_generation_prompt=True)
    inputs = processor(prompt, return_tensors='pt', add_special_tokens=False, truncation=False, return_token_type_ids=False)
    if inputs['input_ids'].shape[1] + maximum > capacity:
        raise ValueError('Conversation exceeds this local model context; start a new chat or shorten your message')
    return inputs


def inputs_for(row, task, processor, length, model, training=False):
    import torch
    if task == 'image-classification':
        inputs = dict(processor(images=image_from_base64(row.get('imageBase64')), return_tensors='pt'))
    else:
        text = text_value(row.get('text'))
        options = {'return_token_type_ids': False} if task == 'text-generation' else {}
        inputs = dict(processor(text, return_tensors='pt', truncation=True, max_length=length, **options))
    if training:
        if task == 'text-generation':
            labels = inputs['input_ids'].clone()
            labels[inputs['attention_mask'] == 0] = -100
            if labels.shape[1] < 2:
                raise ValueError('Training text must contain at least two tokens')
            inputs['labels'] = labels
        else:
            inputs['labels'] = torch.tensor([target_index(row.get('target'), model)])
    return inputs


def run(request, progress=lambda _: None):
    import torch
    if not isinstance(request, dict):
        raise ValueError('Request must be an object')
    action = request.get('action')
    if action not in ('validate', 'infer', 'train'):
        raise ValueError('Unsupported action')
    length = number(request.get('sequenceLength'), 128, 2, 2048, True)
    seed = number(request.get('seed'), 42, 0, 2**32 - 1, True)
    torch.manual_seed(seed)
    torch.set_num_threads(min(4, os.cpu_count() or 1))
    path, task, model, processor = load(request)
    capacity = getattr(model.config, 'max_position_embeddings', getattr(model.config, 'n_positions', 2048))
    if isinstance(capacity, int):
        length = min(length, capacity)
    if task == 'text-generation' and action != 'infer' and length * model.config.vocab_size > 64_000_000:
        raise ValueError('Sequence length exceeds the local output-memory budget; use fewer tokens')
    result = {'ok': True, 'task': task, 'parameterCount': sum(p.numel() for p in model.parameters())}
    result['chatCompatible'] = task == 'text-generation' and bool(getattr(processor, 'chat_template', None))
    if task != 'text-generation':
        result['labels'] = [str(model.config.id2label.get(i, i)) for i in range(model.config.num_labels)]
    if action == 'validate':
        return result
    if action == 'infer':
        with torch.no_grad():
            if task == 'text-generation':
                maximum = number(request.get('maxNewTokens'), 32, 1, 256, True)
                maximum = min(maximum, max(1, capacity - 1))
                messages = request.get('messages')
                if messages is not None:
                    inputs = chat_inputs(processor, messages, min(length, capacity), maximum)
                elif result['chatCompatible']:
                    inputs = chat_inputs(processor, [{'role':'user','content':text_value(request.get('prompt'))}], min(2048, capacity), maximum)
                else:
                    prompt = text_value(request.get('prompt'))
                    inputs = processor(prompt, return_tensors='pt', truncation=True, max_length=min(length, capacity - maximum), return_token_type_ids=False)
                if (inputs['input_ids'].shape[1] + maximum) * model.config.vocab_size > 64_000_000:
                    raise ValueError('Conversation exceeds the local output-memory budget; shorten it or start a new chat')
                generated = model.generate(**inputs, max_new_tokens=maximum, do_sample=False, pad_token_id=processor.pad_token_id)
                result['text'] = processor.decode(generated[0][inputs['input_ids'].shape[1]:], skip_special_tokens=True)
            else:
                row = {'text': request.get('prompt'), 'imageBase64': request.get('imageBase64')}
                logits = model(**inputs_for(row, task, processor, length, model)).logits[0]
                scores = torch.softmax(logits, dim=-1)
                result['predictions'] = [{'label': str(model.config.id2label.get(i, i)), 'score': float(scores[i])} for i in range(len(scores))]
                result['predictions'].sort(key=lambda item: item['score'], reverse=True)
        return result
    splits = request.get('splits')
    if not isinstance(splits, dict):
        raise ValueError('Training requires explicit train, validation and test splits')
    for name in ('train', 'validation', 'test'):
        rows = splits.get(name)
        if not isinstance(rows, list) or len(rows) > 1000 or (name == 'train' and not rows) or not all(isinstance(row, dict) for row in rows):
            raise ValueError('Training requires 1 to 1000 rows; evaluation splits allow up to 1000 rows')
    if task == 'text-generation' and len(splits['train']) < 2:
        raise ValueError('Text generation training requires at least two rows')
    epochs = number(request.get('epochs'), 1, 1, 10, True)
    rate = number(request.get('learningRate'), 0.0001, 0.0000001, 0.1)
    out = request.get('outputDir')
    if not isinstance(out, str) or not out or len(out) > 4096:
        raise ValueError('An output directory is required')
    output = Path(out).resolve()
    if output == path or path in output.parents or output in path.parents or output.exists():
        raise ValueError('Output directory must be new and separate from the source')
    for param in model.parameters():
        param.requires_grad = False
    head = model.get_output_embeddings() if task == 'text-generation' else getattr(model, 'classifier', None)
    if head is None:
        raise ValueError('This model has no supported trainable output head')
    # Tied token embeddings belong to the frozen backbone. Give the output
    # head its own weights before enabling gradients.
    if task == 'text-generation' and getattr(model.config, 'tie_word_embeddings', False):
        import copy
        head = copy.deepcopy(head)
        model.config.tie_word_embeddings = False
        model.set_output_embeddings(head)
    for param in head.parameters():
        param.requires_grad = True
    optimizer = torch.optim.SGD([p for p in model.parameters() if p.requires_grad], lr=rate)

    def evaluate(rows):
        if not rows:
            return None
        model.eval()
        loss, correct = 0.0, 0
        with torch.no_grad():
            for row in rows:
                output_data = model(**inputs_for(row, task, processor, length, model, True))
                loss += float(output_data.loss)
                if task != 'text-generation':
                    correct += int(output_data.logits.argmax(-1).item() == target_index(row.get('target'), model))
        metrics = {'loss': loss / len(rows), 'count': len(rows)}
        if task != 'text-generation':
            metrics['accuracy'] = correct / len(rows)
        return metrics

    for epoch in range(epochs):
        model.eval()
        head.train()
        total = 0.0
        for row in splits['train']:
            optimizer.zero_grad(set_to_none=True)
            loss = model(**inputs_for(row, task, processor, length, model, True)).loss
            if not torch.isfinite(loss):
                raise ValueError('Training loss is not finite')
            loss.backward()
            optimizer.step()
            total += float(loss.detach())
        validation = evaluate(splits['validation'])
        event = {'event': 'progress', 'epoch': epoch + 1, 'epochs': epochs, 'trainLoss': total / len(splits['train'])}
        if validation is not None:
            event['validationLoss'] = validation['loss']
        progress(event)
    result['metrics'] = {'validation': validation, 'test': evaluate(splits['test'])}
    if not splits['validation'] or not splits['test']:
        result['metricsNote'] = 'Empty evaluation splits have no held-out metrics.'
    output.mkdir(parents=True)
    model.save_pretrained(str(output), safe_serialization=True)
    processor.save_pretrained(str(output))
    (output / 'mapping.json').write_text(json.dumps({'id2label': model.config.id2label}), encoding='utf-8')
    result.update(checkpoint=str(output), trainingMode='output-head-only')
    return result


def main():
    try:
        raw = sys.stdin.buffer.readline(MAX_REQUEST + 1)
        if len(raw) > MAX_REQUEST:
            raise ValueError('Request is too large')
        request = json.loads(raw)
        result = run(request, lambda event: print(json.dumps(event, allow_nan=False), flush=True))
    except Exception as exc:
        result = {'ok': False, 'error': str(exc)[:2000] or type(exc).__name__}
    print(json.dumps(result, allow_nan=False), flush=True)


if __name__ == '__main__':
    main()
