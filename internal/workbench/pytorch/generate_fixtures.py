"""Create small actually-trained, exportable local-inference acceptance assets.

This trusted developer utility trains only built-in palette modules. It never
creates application projects/models. The tiny text model memorizes a few lines;
it is an integration fixture, not a capable general-purpose chat model.
"""
import argparse
import csv
import json
from pathlib import Path

import torch
from torch import nn
from safetensors.torch import save_file

from runner import Model, Worker, analyze


def graph(entries):
    return dict(version=1, format="umcode.layers.v1", nodes=[
        dict(id=f"node{i}", kind=kind, position=dict(x=80, y=i*130), config=config)
        for i, (kind, config) in enumerate(entries)], edges=[
            dict(id=f"edge{i}", source=f"node{i-1}", target=f"node{i}")
            for i in range(1, len(entries))])


def selection(config, classes=None, statistics=None, rows=1):
    return dict(version="fixture-preparation-v1", datasetId="fixture-dataset", config={
        "datasetId": "fixture-dataset", "mode": "features", "purpose": "regression",
        "inputColumns": ["x"], "targetColumn": "y", "scale": "none",
        "trainPercent": 100, "validationPercent": 0, "testPercent": 0,
        "seed": 17, "stratify": False, "sequenceLength": 0, "imageWidth": 0,
        "imageHeight": 0, "imageChannels": 0, **config},
        counts=dict(train=rows, validation=0, test=0), classes=classes or [],
        statistics=statistics or [], createdAt="2026-11-01T00:00:00Z", warnings=[])


def write_json(path, data):
    path.write_text(json.dumps(data, indent=2)+"\n")


def new_model(g):
    order, shapes = analyze(g)
    return Model(order, shapes)


def export_model(output, name, model, g, saved):
    state = model.state_dict()
    torch.save(state, output / f"{name}.pt")
    # Clone allows safe serialization of duplicate keys in tied-weight graphs.
    save_file({key: value.detach().contiguous().clone() for key, value in state.items()},
              str(output / f"{name}.safetensors"))
    write_json(output / f"{name}-architecture.json", g)
    write_json(output / f"{name}-selection.json", saved)
    (output / f"{name}-architecture.yaml").write_text(json.dumps(g, indent=2)+"\n")


def train_numeric(output):
    g = graph([("input", dict(mode="features", features=1)),
               ("linear", dict(outFeatures=1, bias=True)), ("output", {})])
    model = new_model(g)
    raw = torch.arange(0., 21.).reshape(-1, 1)
    mean, std = float(raw.mean()), float(raw.std(unbiased=False))
    inputs, target = (raw-mean)/std, 3*raw+2
    optimizer = torch.optim.Adam(model.parameters(), lr=.15)
    for _ in range(1600):
        optimizer.zero_grad()
        loss = nn.functional.mse_loss(model(inputs), target)
        loss.backward()
        optimizer.step()
    saved = selection(dict(scale="standard"), statistics=[dict(column="x", mean=mean, std=std)], rows=len(raw))
    export_model(output, "tiny-numeric", model, g, saved)
    with (output / "tiny-numeric.csv").open("w", newline="") as stream:
        writer = csv.writer(stream)
        writer.writerow(["x", "y"])
        writer.writerows((int(x), int(3*x+2)) for x in range(21))
    return dict(trainingLoss=float(loss.detach()), predictionAtX14=float(model(torch.tensor([[(14-mean)/std]])).detach()[0, 0]))


def train_chat(output, steps, smoke):
    pairs = [
        ("Hello", "Hello! I am a tiny local model."),
        ("Hi", "Hello! I am a tiny local model."),
        ("Who are you?", "I am a tiny trained local demo."),
        ("What can you do?", "I can greet you. I am a tiny demo."),
        ("Thanks", "You are welcome!"),
        ("Bye", "Goodbye!"),
        ("Good morning", "Good morning!"),
        ("What is 2+2?", "2+2=4."),
    ]
    if smoke:
        pairs = pairs[:1]
    length = 128
    g = graph([("input", dict(mode="tokens", sequence=length)),
               ("embedding", dict(vocab=259, width=48)),
               ("position", dict(length=length)),
               ("transformer", dict(width=48, heads=4, expansion=2, repeat=2, dropout=0., causal=True)),
               ("layernorm", {}), ("linear", dict(outFeatures=259, bias=True)), ("output", {})])
    model = new_model(g)
    inputs, labels = [], []
    records = []
    for prompt, response in pairs:
        framed = f"User: {prompt}\nAssistant:"
        # The Data adapter joins prompt and response with a newline. Include the
        # exact newline in training so the saved fixture uses that same recipe.
        source = framed.encode("utf-8")
        text = source + b"\n" + response.encode("utf-8")
        ids = [1] + [value+3 for value in text] + [2]
        target = ids[1:] + [0]
        # Response-only causal loss, including the Data separator and EOS.
        for i in range(len(source)):
            target[i] = -100
        target[-1] = -100
        inputs.append(ids + [0]*(length-len(ids)))
        labels.append(target + [-100]*(length-len(target)))
        records.append(dict(prompt=framed, response=response))
    inputs, labels = torch.tensor(inputs), torch.tensor(labels)
    optimizer = torch.optim.AdamW(model.parameters(), lr=.004, weight_decay=.01)
    model.train()
    for _ in range(steps):
        optimizer.zero_grad()
        logits = model(inputs)
        loss = nn.functional.cross_entropy(logits.reshape(-1, 259), labels.reshape(-1), ignore_index=-100)
        loss.backward()
        nn.utils.clip_grad_norm_(model.parameters(), 1.)
        optimizer.step()
    model.eval()
    saved = selection(dict(mode="text", purpose="text-generation", inputColumns=["prompt"],
                           targetColumn="response", sequenceLength=length), rows=len(pairs))
    export_model(output, "tiny-chat", model, g, saved)
    with (output / "tiny-chat.csv").open("w", newline="") as stream:
        writer = csv.DictWriter(stream, ["prompt", "response"])
        writer.writeheader()
        writer.writerows(records)
    worker = Worker()
    loaded = worker.handle(dict(op="load", graph=g, selection=saved,
        weightsPath=str(output / "tiny-chat.pt"), format="pt"))
    if not loaded["ok"]:
        raise RuntimeError(loaded["error"])
    examples = []
    for prompt, expected in pairs:
        result = worker.handle(dict(op="predict", prompt=f"User: {prompt}\nAssistant:",
                                     maxNewTokens=64, temperature=0))
        examples.append(dict(prompt=prompt, expected=expected, prediction=result))
    return dict(steps=steps, trainingLoss=float(loss.detach()), examples=examples,
                quality="Tiny memorization fixture. Not a useful general-purpose language model.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--steps", type=int, default=700)
    parser.add_argument("--smoke", action="store_true", help="Train only Hello for the regression test")
    args = parser.parse_args()
    if not 1 <= args.steps <= 10000:
        parser.error("steps must be between 1 and 10000")
    torch.set_num_threads(1)
    torch.manual_seed(17)
    args.output.mkdir(parents=True, exist_ok=True)
    metrics = dict(numeric=train_numeric(args.output), chat=train_chat(args.output, args.steps, args.smoke))
    write_json(args.output / "training-results.json", metrics)
    (args.output / "README.md").write_text("""# Real local-inference test assets

These are small **actually trained** CPU checkpoints with the visual builder's
`layers.layer_N` state_dict keys. They contain no executable model code.
`tiny-chat` memorizes eight short prompts. It is a plumbing/acceptance fixture,
not a capable general-purpose chat model; unfamiliar prompts and multi-turn
conversations can produce poor text. `training-results.json` records actual
greedy predictions and training loss. `tiny-numeric` learns y=3x+2.

Import in a temporary project only:

1. Create a Custom feature-vector model. In Build, connect Input (features 1),
   Linear (output features 1, bias on), and Output. Save, import tiny-numeric.csv
   in Data, and choose
   numeric features x -> target y, standard scaling, 100/0/0 split, seed 17,
   and prepare. Import tiny-numeric.pt (or .safetensors) in Inference. x=14
   should predict approximately 44. The saved statistics in selection.json
   are provided for direct runner tests; the app creates its own preparation.
2. Create a text-generation model. In Build set Input sequence 128, Embedding
   vocabulary 259 and width 48, Position length 128, Transformer width 48,
   heads 4, expansion 2, repeat 2, dropout 0 and causal on, Layer norm, then
   Linear output 259, bias on, sharing off, and Output. Save. Import
   tiny-chat.csv in Data, choose text input prompt -> response, sequence 128,
   100/0/0 split, seed 17, and prepare. Import tiny-chat.pt in Inference.
   Enter Hello, temperature 0, output limit 64. The server frames it as
   `User: Hello\\nAssistant:`; expected response is a newline followed by
   `Hello! I am a tiny local model.`. Test successfully, then promote to chat.
3. Remove the temporary project after acceptance. Never seed these fixtures
   into a user's model list.

Both formats contain identical trained parameters. Architecture and selection
JSON files are developer acceptance inputs; users configure layers in Build.
The optional prepared local-inference-example folder avoids manual setup.
The CSV prompt cells already carry User:/Assistant: framing for training;
the UI inference prompt should be plain Hello, because the server frames it.
Image inference accepts single-frame PNG/JPEG/WEBP/BMP, applies bilinear resize,
converts to saved channels, and scales byte pixels to float [0,1].
""")
    print(json.dumps(metrics))


if __name__ == "__main__":
    main()
