"""Trusted CPU training for saved visual graphs and prepared JSONL splits.

The parent supplies private snapshot paths, owns process cancellation, and publishes
the checkpoint only after receiving complete. No project Python is executed.
"""
from contextlib import ExitStack
import json
import math
import os
import stat
import sys

import torch
from torch.nn import functional as F

from runner import (Invalid, MAX_ELEMENTS, MAX_LINE, MAX_TENSORS, MAX_WEIGHTS, Model, analyze,
                    integer, number, object_keys, parse_json, prepare_selection, require)

MAX_PREPARED = 256 * 1024 * 1024
MAX_ROWS = 100_000


def configuration(value):
    object_keys(value, ("epochs", "batchSize", "learningRate", "seed"), "training configuration")
    epochs = integer(value.get("epochs"), 1, 1000, "Epochs")
    batch = integer(value.get("batchSize"), 1, 256, "Batch size")
    rate = value.get("learningRate")
    require(number(rate) and 1e-6 <= rate <= 1, "Learning rate must be between 0.000001 and 1.")
    seed = integer(value.get("seed"), 0, 2147483647, "Seed")
    return epochs, batch, rate, seed


def validate_row(row, config, schema):
    require(type(row) is dict, "Every prepared row must be a JSON object.")
    purpose, mode = config["purpose"], config["mode"]
    if mode == "features":
        inputs = row.get("inputs")
        require(type(inputs) is list and len(inputs) == len(schema["features"]) and
                all(number(value) and abs(value) <= torch.finfo(torch.float32).max for value in inputs),
                "Prepared numeric inputs must contain the saved number of finite float32 features.")
    else:
        inputs, mask = row.get("inputIds"), row.get("attentionMask")
        length = schema["sequenceLength"]
        require(type(inputs) is list and len(inputs) == length and
                all(type(value) is int and 0 <= value < 259 for value in inputs),
                "Prepared token IDs must match the saved sequence length and byte vocabulary.")
        require(type(mask) is list and len(mask) == length and
                all(type(value) is int and value in (0, 1) for value in mask) and
                mask[0] == 1 and inputs[0] == 1 and
                all((mask[i] == 1 and inputs[i] != 0) or (mask[i] == 0 and inputs[i] == 0)
                    for i in range(length)) and mask == sorted(mask, reverse=True),
                "Prepared attention masks must describe a BOS-prefixed sequence followed by padding.")
    if purpose == "text-generation":
        targets = row.get("labels")
        require(type(targets) is list and len(targets) == len(inputs) and
                all(type(value) is int and (value == -100 or
                    (0 <= value < 259 and value == inputs[i] and mask[i] == 1))
                    for i, value in enumerate(targets)) and targets[0] == -100 and
                any(value != -100 for value in targets[1:]),
                "Prepared generation labels must match tokens, mask padding, and include a next-token target.")
    elif purpose == "classification":
        targets = integer(row.get("target"), 0, len(schema["classes"])-1, "Prepared class target")
    else:
        targets = row.get("target")
        require(number(targets) and abs(targets) <= torch.finfo(torch.float32).max,
                "Prepared regression targets must be finite float32 numbers.")
    return inputs, targets


def records(stream):
    while True:
        line = stream.readline(MAX_LINE+1)
        if not line:
            break
        require(len(line) <= MAX_LINE, "Prepared row exceeds the 12 MiB limit.")
        try:
            yield parse_json(line)
        except (ValueError, UnicodeError) as error:
            raise Invalid("Prepared split contains invalid JSON: " + str(error)[:200]) from None


def load_splits(paths, config, schema):
    object_keys(paths, ("train", "validation", "test"), "prepared split paths")
    require(set(paths) == {"train", "validation", "test"}, "Provide all three prepared split paths.")
    # Count and validate streamed rows before allocating fixed-size tensors. Keeping
    # file handles open pins the exact files used during both passes.
    with ExitStack() as stack:
        streams, counts, byte_budget, total_rows = {}, {}, 0, 0
        for name in ("train", "validation", "test"):
            path = paths[name]
            require(type(path) is str and 0 < len(path) <= 4096, "Prepared split path is missing.")
            try:
                fd = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_NONBLOCK", 0))
            except OSError:
                raise Invalid("Cannot open the private prepared " + name + " split.") from None
            stream = stack.enter_context(os.fdopen(fd, "rb"))
            info = os.fstat(stream.fileno())
            require(stat.S_ISREG(info.st_mode), "Prepared splits must be regular files.")
            byte_budget += info.st_size
            require(byte_budget <= MAX_PREPARED, "Prepared splits exceed the 256 MiB budget.")
            streams[name], counts[name] = stream, 0
            consumed = 0
            for row in records(stream):
                consumed = stream.tell()
                require(consumed <= info.st_size, "Prepared split changed while training started.")
                validate_row(row, config, schema)
                counts[name] += 1
                total_rows += 1
                require(total_rows <= MAX_ROWS, "Prepared splits exceed the 100000 row training limit.")
            require(consumed == info.st_size, "Prepared split changed while training started.")
        require(counts["train"] > 0, "The training split is empty; save Data preparation with training rows.")
        width = len(schema["features"]) if config["mode"] == "features" else schema["sequenceLength"]
        input_dtype = torch.float32 if config["mode"] == "features" else torch.long
        target_dtype = torch.float32 if config["purpose"] == "regression" else torch.long
        target_width = width if config["purpose"] == "text-generation" else 1
        tensor_bytes = total_rows * (width * (4 if input_dtype == torch.float32 else 8) +
                                     target_width * (4 if target_dtype == torch.float32 else 8))
        require(byte_budget + tensor_bytes <= MAX_PREPARED, "Prepared data and tensors exceed the 256 MiB budget.")
        datasets = {}
        for name, stream in streams.items():
            count = counts[name]
            inputs = torch.empty((count, width), dtype=input_dtype)
            targets = torch.empty((count, target_width) if config["purpose"] == "text-generation" else (count,), dtype=target_dtype)
            stream.seek(0)
            loaded = 0
            for row in records(stream):
                require(loaded < count, "Prepared split changed while training started.")
                x, y = validate_row(row, config, schema)
                inputs[loaded] = torch.tensor(x, dtype=input_dtype)
                targets[loaded] = torch.tensor(y, dtype=target_dtype)
                loaded += 1
            require(loaded == count, "Prepared split changed while training started.")
            datasets[name] = inputs, targets
        return datasets


def batch_loss(output, targets, purpose):
    require(bool(torch.isfinite(output).all()), "Training produced non-finite predictions; reduce the learning rate or rescale data.")
    if purpose == "regression":
        return F.mse_loss(output[:, 0], targets)
    if purpose == "text-generation":
        return F.cross_entropy(output[:, :-1].reshape(-1, output.shape[-1]),
                               targets[:, 1:].reshape(-1), ignore_index=-100)
    return F.cross_entropy(output, targets)


def evaluate(model, dataset, purpose, batch_size, classes):
    inputs, targets = dataset
    count = len(inputs)
    if count == 0:
        return None
    model.eval()
    total_loss, weight, correct, absolute, squared, target_sum, target_squared = 0., 0, 0, 0., 0., 0., 0.
    # O(classes) counts avoid allocating a potentially huge confusion matrix.
    predicted_counts, actual_counts, true_positive = [0]*classes, [0]*classes, [0]*classes
    with torch.inference_mode():
        for start in range(0, count, batch_size):
            x, y = inputs[start:start+batch_size], targets[start:start+batch_size]
            output = model(x)
            loss = batch_loss(output, y, purpose)
            require(bool(torch.isfinite(loss)), "Evaluation produced a non-finite loss; rescale the data.")
            if purpose == "regression":
                difference = output[:, 0].double()-y.double()
                squared += float(difference.square().sum())
                absolute += float(difference.abs().sum())
                target_sum += float(y.double().sum())
                target_squared += float(y.double().square().sum())
                weight += len(y)
            else:
                if purpose == "text-generation":
                    y = y[:, 1:]
                    predictions = output[:, :-1].argmax(dim=-1)
                    mask = y != -100
                    predictions, y = predictions[mask], y[mask]
                else:
                    predictions = output.argmax(dim=-1)
                    for predicted, actual in zip(predictions.tolist(), y.tolist()):
                        predicted_counts[predicted] += 1
                        actual_counts[actual] += 1
                        if predicted == actual:
                            true_positive[actual] += 1
                n = y.numel()
                correct += int((predictions == y).sum())
                total_loss += float(loss)*n
                weight += n
    require(weight > 0, "Prepared split contains no usable training targets.")
    result = dict(loss=squared/weight if purpose == "regression" else total_loss/weight, samples=count)
    if purpose == "regression":
        variance = max(0., target_squared-target_sum*target_sum/weight)
        result.update(mae=absolute/weight, rmse=math.sqrt(squared/weight),
                      r2=1-squared/variance if variance > 0 else None)
    elif purpose == "text-generation":
        loss = result["loss"]
        result.update(tokenAccuracy=correct/weight, perplexity=math.exp(loss) if loss < 709 else None)
    else:
        present = [i for i in range(classes) if actual_counts[i]+predicted_counts[i] > 0]
        result.update(accuracy=correct/weight,
                      macroF1=sum(2*true_positive[i]/(actual_counts[i]+predicted_counts[i]) for i in present)/len(present))
    return result


def train(request, emit):
    require(type(request) is dict, "Training request must be a JSON object.")
    epochs, batch, rate, seed = configuration(request.get("config"))
    order, shapes = analyze(request.get("graph"))
    saved = request.get("selection")
    require(type(saved) is dict and type(saved.get("config")) is dict, "Save Data preparation before training.")
    require(saved["config"].get("mode") != "image", "Image training is not supported yet; choose numeric or text data.")
    config, schema, _, compatible = prepare_selection(saved, order, shapes)
    require(config["purpose"] != "text-generation" or compatible,
            "Text generation training requires causal attention; turn causal masking on.")
    activations = sum(math.prod(shape) for shape in shapes)
    for node, shape in zip(order, shapes):
        if node["kind"] in ("attention", "transformer"):
            c = node["config"]
            repeats = c["repeat"] if node["kind"] == "transformer" else 1
            # Account for saved Q/K/V, attention scores, residuals, and expanded
            # MLP intermediates in every repeated block during backpropagation.
            activations += repeats*(shape[0]*shape[0]*c["heads"]*2 +
                                    math.prod(shape)*(9+2*c.get("expansion", 0)))
    max_batch = request.get("maxBatchSize", 0)
    require(type(max_batch) is int and 0 <= max_batch <= 256, "Invalid training batch preflight limit.")
    if max_batch:
        require(batch <= max_batch,
                f"Batch size exceeds the current local memory or compute budget; use {max_batch} or less.")
    else:
        require(activations*batch <= MAX_ELEMENTS,
                "Training batch activations exceed the fallback 8 million element limit; reduce batch size or architecture size.")
    path = request.get("weightsPath")
    require(type(path) is str and 0 < len(path) <= 4096 and not os.path.lexists(path),
            "Training requires a new private checkpoint path.")
    torch.set_num_threads(1)
    torch.manual_seed(seed)
    # Metadata validation avoids allocating parameter tensors for invalid models.
    with torch.device("meta"):
        metadata = Model(order, shapes)
    state = metadata.state_dict()
    require(len(state) <= MAX_TENSORS, "Graph has too many checkpoint tensors; use fewer repeated layers.")
    require(sum(value.numel()*value.element_size() for value in state.values()) <= MAX_WEIGHTS,
            "Graph tensor data exceeds the 64 MiB checkpoint limit.")
    require(any(parameter.numel() for parameter in metadata.parameters()), "Add a trainable layer before training.")
    del metadata, state
    datasets = load_splits(request.get("splitPaths"), config, schema)
    model = Model(order, shapes)
    optimizer = torch.optim.AdamW(model.parameters(), lr=rate)
    purpose, classes = config["purpose"], len(schema["classes"])
    inputs, targets = datasets["train"]
    for epoch in range(1, epochs+1):
        model.train()
        indices = torch.randperm(len(inputs))
        for start in range(0, len(inputs), batch):
            selected = indices[start:start+batch]
            optimizer.zero_grad(set_to_none=True)
            loss = batch_loss(model(inputs[selected]), targets[selected], purpose)
            require(bool(torch.isfinite(loss)), "Training loss is non-finite; reduce the learning rate or rescale data.")
            loss.backward()
            require(all(parameter.grad is None or bool(torch.isfinite(parameter.grad).all()) for parameter in model.parameters()),
                    "Training gradients are non-finite; reduce the learning rate or rescale data.")
            optimizer.step()
        train_metrics = evaluate(model, datasets["train"], purpose, batch, classes)
        validation = evaluate(model, datasets["validation"], purpose, batch, classes)
        emit(dict(type="epoch", epoch=epoch, train=train_metrics, validation=validation))
    test = evaluate(model, datasets["test"], purpose, batch, classes)
    require(all(bool(torch.isfinite(value).all()) for value in model.state_dict().values()),
            "Trained weights are not finite; reduce the learning rate.")
    result = dict(type="complete", train=train_metrics, validation=validation, test=test,
                  parameterCount=sum(parameter.numel() for parameter in model.parameters()))
    # Serialize only a tensor state_dict, after every training/evaluation step has
    # succeeded. The parent owns cleanup if cancellation interrupts this write.
    with open(path, "xb") as stream:
        torch.save(model.state_dict(), stream)
    return result


def main():
    def emit(value):
        sys.stdout.write(json.dumps(value, allow_nan=False, separators=(",", ":"))+"\n")
        sys.stdout.flush()
    try:
        line = sys.stdin.buffer.readline(MAX_LINE+1)
        require(0 < len(line) <= MAX_LINE, "Training request exceeds the 12 MiB IPC limit or is empty.")
        emit(train(parse_json(line), emit))
    except Invalid as error:
        emit(dict(type="error", error=str(error)))
    except (ValueError, UnicodeError):
        emit(dict(type="error", error="Training request and prepared rows must contain valid finite JSON data."))
    except Exception:
        emit(dict(type="error", error="Local training failed. Check saved architecture, prepared splits, disk space, and training limits."))


if __name__ == "__main__":
    main()
