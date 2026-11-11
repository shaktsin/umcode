import tempfile
import unittest
from pathlib import Path

import imported
from moe_fixture import create


class LoRATests(unittest.TestCase):
    def test_answers_use_chat_template_mask_prompt_and_reject_truncation(self):
        with tempfile.TemporaryDirectory() as tmp:
            create(tmp, 'qwen3_moe')
            _, _, model, tokenizer = imported.load({'task': 'text-generation', 'bundleDir': tmp})
            row = {'text': 'hello', 'response': 'world reply'}
            inputs = imported.inputs_for(row, 'text-generation', tokenizer, 32, model, True)
            self.assertEqual(inputs['input_ids'].tolist(), [[5, 3, 6, 4, 7, 1]])
            self.assertEqual(inputs['labels'].tolist(), [[-100, -100, -100, 4, 7, 1]])
            with self.assertRaisesRegex(ValueError, 'sequence length'):
                imported.inputs_for(row, 'text-generation', tokenizer, 4, model, True)

    def test_lora_trains_attention_only_and_merged_checkpoint_reloads(self):
        import torch
        from safetensors.torch import load_file
        for family in ['mixtral', 'granitemoe', 'qwen2_moe', 'qwen3_moe']:
            with self.subTest(family=family), tempfile.TemporaryDirectory() as tmp:
                source, output = Path(tmp) / 'source', Path(tmp) / 'output'
                create(source, family)
                common = {'task': 'text-generation', 'bundleDir': str(source), 'sequenceLength': 32}
                _, _, base, _ = imported.load(common)
                adapted = imported.with_lora(base)
                trainable = [n for n, p in adapted.named_parameters() if p.requires_grad]
                self.assertTrue(trainable)
                self.assertTrue(all('lora_' in n and ('q_proj' in n or 'v_proj' in n) for n in trainable))
                rows = [{'text': 'hello', 'response': 'world reply'}, {'text': 'world', 'response': 'hello reply'}]
                result = imported.run({**common, 'action': 'train', 'outputDir': str(output),
                                       'splits': {'train': rows, 'validation': rows, 'test': rows}})
                self.assertEqual(result['trainingMode'], 'lora')
                self.assertLessEqual(result['metrics']['validation']['loss'], result['baselineValidation']['loss'])
                self.assertIn('perplexity', result['metrics']['test'])
                self.assertFalse((output / 'adapter_config.json').exists())
                original, saved = load_file(source / 'model.safetensors'), load_file(output / 'model.safetensors')
                self.assertEqual(set(original), set(saved))
                for key, value in original.items():
                    if '.q_proj.' not in key and '.v_proj.' not in key:
                        self.assertTrue(torch.equal(value, saved[key]), key)
                self.assertTrue(imported.run({**common, 'action': 'infer', 'checkpointDir': str(output),
                                             'prompt': 'hello', 'maxNewTokens': 2})['ok'])


if __name__ == '__main__':
    unittest.main()
