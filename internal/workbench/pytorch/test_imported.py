import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).parent


class ProtocolTests(unittest.TestCase):
    def test_allocation_budget_before_loading_weights_or_processing_images(self):
        import imported
        from transformers import GPT2Config
        with self.assertRaisesRegex(ValueError, 'budget'):
            imported.check_config_budget(GPT2Config(n_layer=100000, n_embd=1000000))
        with self.assertRaisesRegex(ValueError, 'budget'):
            imported.check_processor_budget({'size': {'height': 1000000, 'width': 1000000}})
        with self.assertRaisesRegex(ValueError, 'budget'):
            imported.check_processor_budget({'crop_size': {'height': 8192, 'width': 8192}})
        imported.check_config_budget(GPT2Config(n_layer=2, n_embd=32, n_head=2))
        imported.check_processor_budget({'size': {'shortest_edge': 224}, 'crop_pct': .875})
    def test_missing_bundle_returns_json_error_without_traceback(self):
        result = subprocess.run([sys.executable, str(HERE / 'imported.py')],
                                input=json.dumps({'action': 'validate', 'bundleDir': '/nonexistent', 'task': 'text-generation'}),
                                text=True, capture_output=True)
        self.assertFalse(json.loads(result.stdout.splitlines()[-1])['ok'])
        self.assertNotIn('Traceback', result.stderr)

    def test_resnet_inference_training_and_checkpoint(self):
        import base64
        import io
        import torch
        from PIL import Image
        from transformers import ResNetConfig, ResNetForImageClassification, ConvNextImageProcessor

        with tempfile.TemporaryDirectory() as tmp:
            bundle = Path(tmp) / 'bundle'
            output = Path(tmp) / 'checkpoint'
            bundle.mkdir()
            config = ResNetConfig(embedding_size=8, hidden_sizes=[8, 16], depths=[1, 1], num_labels=2)
            ResNetForImageClassification(config).save_pretrained(bundle, safe_serialization=True)
            ConvNextImageProcessor(size={'shortest_edge': 32}, crop_pct=1.0).save_pretrained(bundle)
            buf = io.BytesIO()
            Image.new('RGB', (32, 32), 'red').save(buf, format='PNG')
            image = base64.b64encode(buf.getvalue()).decode()
            def run(payload):
                p = subprocess.run([sys.executable, str(HERE / 'imported.py')], input=json.dumps(payload), text=True, capture_output=True)
                return [json.loads(line) for line in p.stdout.splitlines()]
            common = {'bundleDir': str(bundle), 'task': 'image-classification'}
            validated = run({**common, 'action': 'validate'})[-1]
            self.assertEqual(validated['labels'], ['LABEL_0', 'LABEL_1'])
            inferred = run({**common, 'action': 'infer', 'imageBase64': image})[-1]
            self.assertTrue(inferred['ok'], inferred)
            self.assertEqual(len(inferred['predictions']), 2)
            rows = [{'imageBase64': image, 'target': 0}, {'imageBase64': image, 'target': 1}]
            trained = run({**common, 'action': 'train', 'outputDir': str(output), 'epochs': 1,
                           'splits': {'train': rows, 'validation': rows, 'test': rows}})
            self.assertTrue(trained[-1]['ok'], trained)
            self.assertTrue(any(x.get('event') == 'progress' for x in trained))
            self.assertIn('test', trained[-1]['metrics'])
            self.assertTrue((output / 'model.safetensors').exists())
            self.assertTrue((bundle / 'model.safetensors').exists())
            from safetensors.torch import load_file
            original = load_file(bundle / 'model.safetensors')
            updated = load_file(output / 'model.safetensors')
            for key, tensor in original.items():
                if not key.startswith('classifier.'):
                    self.assertTrue(torch.equal(tensor, updated[key]), 'Frozen backbone changed: ' + key)
            rejected = run({**common, 'action': 'train', 'outputDir': str(Path(tmp) / 'bad'),
                            'splits': {'train': [{'imageBase64': image, 'target': 2}], 'validation': rows, 'test': rows}})
            self.assertFalse(rejected[-1]['ok'])
            named = run({**common, 'action': 'train', 'outputDir': str(Path(tmp) / 'named'),
                         'splits': {'train': [{'imageBase64': image, 'target': 'LABEL_0'}], 'validation': [], 'test': []}})
            self.assertTrue(named[-1]['ok'], named)
            self.assertIsNone(named[-1]['metrics']['test'])

    def test_gpt2_generation_and_training_reload(self):
        from tokenizers import Tokenizer
        from tokenizers.models import WordLevel
        from tokenizers.pre_tokenizers import Whitespace
        from transformers import GPT2Config, GPT2LMHeadModel, PreTrainedTokenizerFast
        with tempfile.TemporaryDirectory() as tmp:
            bundle, output = Path(tmp) / 'bundle', Path(tmp) / 'output'
            bundle.mkdir()
            tokenizer = Tokenizer(WordLevel({'[PAD]': 0, '[UNK]': 1, 'hello': 2, 'world': 3}, unk_token='[UNK]'))
            tokenizer.pre_tokenizer = Whitespace()
            PreTrainedTokenizerFast(tokenizer_object=tokenizer, pad_token='[PAD]', unk_token='[UNK]').save_pretrained(bundle)
            GPT2LMHeadModel(GPT2Config(vocab_size=4, n_positions=16, n_embd=8, n_layer=1, n_head=2)).save_pretrained(bundle, safe_serialization=True)
            def run(payload):
                p = subprocess.run([sys.executable, str(HERE / 'imported.py')], input=json.dumps(payload), text=True, capture_output=True)
                return [json.loads(line) for line in p.stdout.splitlines()][-1]
            common = {'bundleDir': str(bundle), 'task': 'text-generation', 'sequenceLength': 8}
            self.assertTrue(run({**common, 'action': 'infer', 'prompt': 'hello world', 'maxNewTokens': 2})['ok'])
            rows = [{'text': 'hello world'}, {'text': 'world hello'}]
            trained = run({**common, 'action': 'train', 'outputDir': str(output), 'splits': {'train': rows, 'validation': rows, 'test': rows}})
            self.assertTrue(trained['ok'], trained)
            self.assertEqual(trained['trainingMode'], 'output-head-only')
            import torch
            from safetensors.torch import load_file
            original = load_file(bundle / 'model.safetensors')
            updated = load_file(output / 'model.safetensors')
            for key, tensor in original.items():
                if not key.startswith('lm_head.'):
                    self.assertTrue(torch.equal(tensor, updated[key]), 'Frozen backbone changed: ' + key)
            self.assertTrue(run({**common, 'action': 'validate', 'checkpointDir': str(output)})['ok'])
            one = run({**common, 'action': 'train', 'outputDir': str(Path(tmp) / 'one'),
                       'splits': {'train': rows[:1], 'validation': [], 'test': []}})
            self.assertFalse(one['ok'])

    def test_incomplete_weight_file_is_rejected(self):
        from safetensors.torch import save_file
        from transformers import GPT2Config, PreTrainedTokenizerFast
        from tokenizers import Tokenizer
        from tokenizers.models import WordLevel
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp)
            GPT2Config(vocab_size=4, n_positions=16, n_embd=8, n_layer=1, n_head=2).save_pretrained(path)
            save_file({}, path / 'model.safetensors')
            PreTrainedTokenizerFast(tokenizer_object=Tokenizer(WordLevel({'[PAD]': 0}, unk_token='[PAD]')), pad_token='[PAD]').save_pretrained(path)
            p = subprocess.run([sys.executable, str(HERE / 'imported.py')], input=json.dumps({'action': 'validate', 'bundleDir': str(path), 'task': 'text-generation'}), text=True, capture_output=True)
            self.assertFalse(json.loads(p.stdout.splitlines()[-1])['ok'])

    def test_bert_classification_uses_checkpoint_labels(self):
        from tokenizers import Tokenizer
        from tokenizers.models import WordLevel
        from tokenizers.pre_tokenizers import Whitespace
        from transformers import BertConfig, BertForSequenceClassification, PreTrainedTokenizerFast
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp)
            tokenizer = Tokenizer(WordLevel({'[PAD]': 0, '[UNK]': 1, 'hello': 2}, unk_token='[UNK]'))
            tokenizer.pre_tokenizer = Whitespace()
            PreTrainedTokenizerFast(tokenizer_object=tokenizer, pad_token='[PAD]', unk_token='[UNK]').save_pretrained(path)
            BertForSequenceClassification(BertConfig(vocab_size=3, hidden_size=8, num_hidden_layers=1,
                                                       num_attention_heads=2, intermediate_size=16,
                                                       id2label={0: 'negative', 1: 'positive'})).save_pretrained(path)
            p = subprocess.run([sys.executable, str(HERE / 'imported.py')], input=json.dumps({
                'action': 'infer', 'bundleDir': str(path), 'task': 'text-classification', 'prompt': 'hello'}), text=True, capture_output=True)
            result = json.loads(p.stdout.splitlines()[-1])
            self.assertTrue(result['ok'], result)
            self.assertEqual(result['labels'], ['negative', 'positive'])
            self.assertEqual({x['label'] for x in result['predictions']}, {'negative', 'positive'})


if __name__ == '__main__':
    unittest.main()
