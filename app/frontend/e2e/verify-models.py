"""Verify generated models with real PyTorch, including causal behavior."""
import importlib.util
import json
import pathlib
import sys
import torch

root = pathlib.Path(sys.argv[1])
manifest = json.loads((root / 'manifest.json').read_text())
for name, expected in manifest.items():
    spec = importlib.util.spec_from_file_location(name, root / f'{name}.py')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    model = module.Model().eval()
    tokens = torch.randint(0, 32, expected['inputShape']) if expected['inputType'] == 'tokens' else torch.randn(expected['inputShape'])
    with torch.no_grad():
        output = model(tokens)
    assert list(output.shape) == expected['outputShape'], (name, output.shape)
    assert sum(p.numel() for p in model.parameters()) == expected['parameters'], name
    assert torch.isfinite(output).all(), name
    model.train()
    model.zero_grad()
    model(tokens).square().mean().backward()
    assert all(p.grad is not None and torch.isfinite(p.grad).all() for p in model.parameters()), name
    model.eval()
    if name == 'text':
        changed = tokens.clone()
        changed[:, 4:] = (changed[:, 4:] + 1) % 32
        with torch.no_grad():
            torch.testing.assert_close(output[:, :4], model(changed)[:, :4])
    print(f'{name}: CPU forward {list(output.shape)}, {expected["parameters"]:,} parameters, PASS')
