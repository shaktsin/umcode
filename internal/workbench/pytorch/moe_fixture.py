"""Create tiny, RANDOM-WEIGHT MoE folders for workflow tests, not answer quality."""
import argparse
from pathlib import Path


def create(folder, family):
    import torch
    from transformers import (MixtralConfig, MixtralForCausalLM, GraniteMoeConfig, GraniteMoeForCausalLM,
                              Qwen2MoeConfig, Qwen2MoeForCausalLM, Qwen3MoeConfig, Qwen3MoeForCausalLM,
                              PreTrainedTokenizerFast)
    from tokenizers import Tokenizer
    from tokenizers.models import WordLevel
    from tokenizers.pre_tokenizers import Whitespace
    constructors = {'mixtral': (MixtralConfig, MixtralForCausalLM), 'granitemoe': (GraniteMoeConfig, GraniteMoeForCausalLM),
                    'qwen2_moe': (Qwen2MoeConfig, Qwen2MoeForCausalLM), 'qwen3_moe': (Qwen3MoeConfig, Qwen3MoeForCausalLM)}
    config_type, model_type = constructors[family]
    torch.manual_seed(42)
    config = config_type(vocab_size=8, hidden_size=8, intermediate_size=16, num_hidden_layers=1,
                         num_attention_heads=2, num_key_value_heads=1, head_dim=4, max_position_embeddings=128,
                         num_local_experts=2, num_experts=2, num_experts_per_tok=1, moe_intermediate_size=16,
                         shared_expert_intermediate_size=16, tie_word_embeddings=False, pad_token_id=0,
                         eos_token_id=1, bos_token_id=2)
    raw = Tokenizer(WordLevel({'[PAD]':0, '[EOS]':1, '[UNK]':2, 'hello':3, 'world':4, 'user':5, 'assistant':6, 'reply':7}, unk_token='[UNK]'))
    raw.pre_tokenizer = Whitespace()
    tokenizer = PreTrainedTokenizerFast(tokenizer_object=raw, pad_token='[PAD]', eos_token='[EOS]', unk_token='[UNK]')
    tokenizer.chat_template = "{% for m in messages %}{{ m['role'] + ' ' + m['content'] + ' ' }}{% endfor %}{% if add_generation_prompt %}{{ 'assistant' }}{% endif %}"
    folder = Path(folder)
    folder.mkdir(parents=True, exist_ok=True)
    model_type(config).save_pretrained(folder, safe_serialization=True)
    tokenizer.save_pretrained(folder)
    return folder


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('folder')
    parser.add_argument('family', choices=['mixtral','granitemoe','qwen2_moe','qwen3_moe'])
    args = parser.parse_args()
    create(args.folder, args.family)
