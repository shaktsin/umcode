"""Trusted, CPU-only local inference. No project Python is imported or evaluated.

stdin/stdout carry bounded JSON lines; the parent owns checkpoint paths and process
lifetime. All graph sizes and checkpoint metadata are checked before allocation.
"""
import base64
import binascii
from collections import OrderedDict
import io
import json
import math
import os
import re
import stat
import struct
import sys
import warnings
import zipfile

import torch
from torch import nn

MAX_WEIGHTS = 64 * 1024 * 1024
MAX_HEADER = 1024 * 1024
MAX_LINE = 12 * 1024 * 1024
MAX_PARAMETERS = 16_000_000
MAX_ELEMENTS = 8_000_000
MAX_WORK = 500_000_000
MAX_IMAGE_BYTES = 8 * 1024 * 1024
MAX_IMAGE_PIXELS = 4_000_000
MAX_TENSORS = 20_000
MAX_NEW_TOKENS = 256
DEFAULTS = {
    "input": dict(mode="tokens", sequence=8, features=16, channels=3, height=32, width=32),
    "embedding": dict(vocab=32, width=16), "position": dict(length=128),
    "linear": dict(outFeatures=64, bias=True, tieEmbedding=False),
    "attention": dict(heads=4, dropout=.1, causal=True),
    "transformer": dict(width=16, heads=4, expansion=4, repeat=2, dropout=.1, causal=True),
    "layernorm": {}, "gelu": {}, "relu": {}, "dropout": dict(dropout=.1), "meanpool": {},
    "output": {}, "conv2d": dict(channels=16, kernel=3, stride=1, padding=1, bias=True),
    "batchnorm2d": {}, "maxpool2d": dict(kernel=2, stride=2, padding=0),
    "avgpool2d": dict(kernel=2, stride=2, padding=0), "globalpool2d": {},
    "flatten": {}, "patchembedding": dict(patch=4, width=32),
}


class Invalid(ValueError):
    """An actionable, safe-to-display validation error."""


def require(condition, message):
    if not condition:
        raise Invalid(message)


def number(value):
    return type(value) in (int, float) and math.isfinite(value)


def integer(value, low, high, name):
    require(type(value) is int and low <= value <= high,
            f"{name} must be an integer between {low} and {high}.")
    return value


def object_keys(value, allowed, name):
    require(type(value) is dict and set(value) <= set(allowed), f"Invalid {name} fields.")


def bounded_shape(shape):
    require(all(type(x) is int and x > 0 for x in shape), "Graph has an invalid tensor shape.")
    require(math.prod(shape) <= MAX_ELEMENTS, "Graph exceeds the 8 million activation element limit.")


def analyze(graph):
    object_keys(graph, ("version", "format", "nodes", "edges"), "graph")
    require(graph.get("version") == 1 and graph.get("format") == "umcode.layers.v1",
            "Use a saved umcode.layers.v1 architecture.")
    nodes, edges = graph.get("nodes"), graph.get("edges")
    require(type(nodes) is list and 2 <= len(nodes) <= 200 and type(edges) is list and len(edges) <= 200,
            "Graph must have 2 to 200 layers and at most 200 connections.")
    by_id, outgoing, incoming = {}, {}, {}
    for node in nodes:
        object_keys(node, ("id", "kind", "position", "config"), "layer")
        node_id, kind = node.get("id"), node.get("kind")
        require(type(node_id) is str and re.fullmatch(r"[a-zA-Z][\w-]{0,63}", node_id) and node_id not in by_id,
                "Layer IDs must be unique builder identifiers.")
        require(type(kind) is str and kind in DEFAULTS, "Architecture contains an unsupported layer.")
        position = node.get("position")
        object_keys(position, ("x", "y"), "layer position")
        require(number(position.get("x")) and number(position.get("y")), "Invalid layer position.")
        config = node.get("config")
        object_keys(config, DEFAULTS[kind], "layer configuration")
        c = dict(DEFAULTS[kind], **config)
        for key, default in DEFAULTS[kind].items():
            value = c[key]
            if type(default) is bool:
                require(type(value) is bool, f"{key} must be on or off.")
            elif type(default) is str:
                require(value in ("tokens", "features", "image"), "Choose tokens, features, or image input.")
            elif key == "dropout":
                require(number(value) and 0 <= value <= .95, "Dropout must be between 0 and 0.95.")
            else:
                bounds = {"vocab": (1, 1_000_000), "outFeatures": (1, 1_000_000),
                          "sequence": (1, 32768), "length": (1, 32768), "heads": (1, 256),
                          "expansion": (1, 16), "repeat": (1, 48), "kernel": (1, 31),
                          "stride": (1, 16), "padding": (0, 32), "patch": (1, 128)}
                low, high = bounds.get(key, (1, 65536))
                if kind == "input":
                    high = {"channels": 512, "height": 4096, "width": 4096}.get(key, high)
                integer(value, low, high, key)
        by_id[node_id] = dict(node, config=c)
    edge_ids = set()
    for edge in edges:
        object_keys(edge, ("id", "source", "target"), "connection")
        source, target, edge_id = edge.get("source"), edge.get("target"), edge.get("id")
        require(type(edge_id) is str and edge_id not in edge_ids and type(source) is str and
                type(target) is str and source in by_id and target in by_id and source != target,
                "Invalid or duplicate graph connection.")
        require(source not in outgoing and target not in incoming, "Only a single Input-to-Output path is supported.")
        require(by_id[source]["kind"] != "output" and by_id[target]["kind"] != "input", "Invalid Input/Output connection.")
        edge_ids.add(edge_id)
        outgoing[source], incoming[target] = target, source
    inputs = [n for n in by_id.values() if n["kind"] == "input"]
    outputs = [n for n in by_id.values() if n["kind"] == "output"]
    require(len(inputs) == len(outputs) == 1, "Graph needs exactly one Input and one Output.")
    order, seen, node = [], set(), inputs[0]
    while node["id"] not in seen:
        seen.add(node["id"])
        order.append(node)
        next_id = outgoing.get(node["id"])
        if next_id is None:
            break
        node = by_id[next_id]
    require(len(order) == len(nodes) and order[-1]["kind"] == "output", "Connect every layer in one acyclic Input-to-Output path.")
    shapes, parameters, work, embedding = [], 0, 0, None
    dtype = "tokens"
    for i, node in enumerate(order):
        kind, c = node["kind"], node["config"]
        if kind == "input":
            mode = c["mode"]
            dims = [c["sequence"]] if mode == "tokens" else [c["features"]] if mode == "features" else [c["channels"], c["height"], c["width"]]
            dtype = "tokens" if mode == "tokens" else "float"
            bounded_shape(dims)
            shapes.append(dims)
            continue
        previous, dims = dims, list(dims)
        width = previous[-1]
        if kind == "embedding":
            require(dtype == "tokens", "Embedding needs token IDs directly after Input.")
            parameters += c["vocab"] * c["width"]
            dims, dtype = previous + [c["width"]], "float"
            if embedding is None:
                embedding = (i, c)
        elif kind != "output":
            require(dtype == "float", "Add an Embedding after token Input.")
            if kind == "linear":
                require(len(previous) != 3, "Flatten or global-pool images before Linear.")
                if c["tieEmbedding"]:
                    require(embedding and embedding[1]["width"] == width and embedding[1]["vocab"] == c["outFeatures"] and not c["bias"],
                            "Shared weights need matching Embedding width/vocabulary with Linear bias off.")
                else:
                    parameters += width * c["outFeatures"] + (c["outFeatures"] if c["bias"] else 0)
                work += math.prod(previous) * c["outFeatures"]
                dims[-1] = c["outFeatures"]
            elif kind == "position":
                require(len(previous) == 2 and previous[0] <= c["length"], "Position embedding must cover the input sequence.")
                parameters += c["length"] * width
            elif kind in ("attention", "transformer"):
                require(len(previous) == 2 and width % c["heads"] == 0, "Attention needs a sequence with width divisible by heads.")
                sequence = previous[0]
                require(sequence * sequence * c["heads"] <= MAX_ELEMENTS, "Attention exceeds the 8 million score element limit.")
                if kind == "transformer":
                    require(c["width"] == width, "Transformer width must match incoming features.")
                    parameters += c["repeat"] * ((4 + 2*c["expansion"])*width*width + (9+c["expansion"])*width)
                    work += c["repeat"] * ((4+2*c["expansion"])*sequence*width*width + 2*sequence*sequence*width)
                else:
                    parameters += 4*width*width + 4*width
                    work += 4*sequence*width*width + 2*sequence*sequence*width
            elif kind == "layernorm":
                require(len(previous) != 3, "Use BatchNorm2d for image tensors.")
                parameters += 2*width
            elif kind == "meanpool":
                require(len(previous) == 2, "Mean pool needs a sequence of vectors.")
                dims = [width]
            elif kind == "flatten":
                dims = [math.prod(previous)]
            elif kind in ("conv2d", "batchnorm2d", "maxpool2d", "avgpool2d", "globalpool2d", "patchembedding"):
                require(len(previous) == 3, "Vision layers need [channels, height, width] image tensors.")
                channels, height, image_width = previous
                if kind == "batchnorm2d":
                    parameters += 2*channels
                elif kind == "globalpool2d":
                    dims = [channels]
                elif kind == "patchembedding":
                    patch = c["patch"]
                    require(height % patch == image_width % patch == 0, "Image dimensions must be divisible by patch size.")
                    dims = [(height//patch)*(image_width//patch), c["width"]]
                    parameters += c["width"]*channels*patch*patch + c["width"]
                    work += math.prod(dims)*channels*patch*patch
                else:
                    kernel, stride, padding = c["kernel"], c["stride"], c["padding"]
                    require(kind == "conv2d" or padding <= kernel//2, "Pooling padding must be at most half the kernel.")
                    h, w = (height+2*padding-kernel)//stride+1, (image_width+2*padding-kernel)//stride+1
                    require(h > 0 and w > 0, "Kernel is too large for the image.")
                    out = c["channels"] if kind == "conv2d" else channels
                    dims = [out, h, w]
                    if kind == "conv2d":
                        parameters += out*channels*kernel*kernel + (out if c["bias"] else 0)
                        work += out*h*w*channels*kernel*kernel
        bounded_shape(dims)
        require(parameters <= MAX_PARAMETERS, "Graph exceeds the 16 million parameter limit.")
        require(work <= MAX_WORK, "Graph exceeds the CPU inference operation limit; use a smaller architecture.")
        shapes.append(dims)
    return order, shapes


class SelfAttention(nn.Module):
    def __init__(self, width, heads, dropout, causal=True):
        super().__init__()
        self.causal = causal
        self.attention = nn.MultiheadAttention(width, heads, dropout=dropout, batch_first=True)

    def forward(self, x):
        mask = torch.ones(x.size(1), x.size(1), device=x.device, dtype=torch.bool).triu(1) if self.causal else None
        return self.attention(x, x, x, attn_mask=mask, need_weights=False)[0]


class TransformerBlock(nn.Module):
    def __init__(self, width, heads, expansion, dropout, causal=True):
        super().__init__()
        self.norm1 = nn.LayerNorm(width)
        self.attention = SelfAttention(width, heads, dropout, causal)
        self.norm2 = nn.LayerNorm(width)
        self.mlp = nn.Sequential(nn.Linear(width, width*expansion), nn.GELU(), nn.Linear(width*expansion, width), nn.Dropout(dropout))

    def forward(self, x):
        x = x + self.attention(self.norm1(x))
        return x + self.mlp(self.norm2(x))


class PositionEmbedding(nn.Module):
    def __init__(self, length, width):
        super().__init__()
        self.embedding = nn.Embedding(length, width)

    def forward(self, x):
        return x + self.embedding(torch.arange(x.size(1), device=x.device)).unsqueeze(0)


class MeanPool(nn.Module):
    def forward(self, x):
        return x.mean(dim=1)


class PatchEmbedding(nn.Module):
    def __init__(self, channels, width, patch):
        super().__init__()
        self.projection = nn.Conv2d(channels, width, kernel_size=patch, stride=patch)

    def forward(self, x):
        return self.projection(x).flatten(2).transpose(1, 2)


class Model(nn.Module):
    def __init__(self, order, shapes):
        super().__init__()
        self.layers = nn.ModuleDict()
        embedding_key = None
        for i, node in enumerate(order):
            kind, c = node["kind"], node["config"]
            if kind in ("input", "output"):
                continue
            previous, width = shapes[i-1], shapes[i-1][-1]
            if kind == "embedding":
                layer = nn.Embedding(c["vocab"], c["width"])
                if embedding_key is None:
                    embedding_key = f"layer_{i}"
            elif kind == "position": layer = PositionEmbedding(c["length"], width)
            elif kind == "linear": layer = nn.Linear(width, c["outFeatures"], bias=c["bias"])
            elif kind == "attention": layer = SelfAttention(width, c["heads"], c["dropout"], c["causal"])
            elif kind == "transformer": layer = nn.Sequential(*[TransformerBlock(width, c["heads"], c["expansion"], c["dropout"], c["causal"]) for _ in range(c["repeat"])])
            elif kind == "layernorm": layer = nn.LayerNorm(width)
            elif kind == "dropout": layer = nn.Dropout(c["dropout"])
            elif kind == "meanpool": layer = MeanPool()
            elif kind == "gelu": layer = nn.GELU()
            elif kind == "relu": layer = nn.ReLU()
            elif kind == "conv2d": layer = nn.Conv2d(previous[0], c["channels"], kernel_size=c["kernel"], stride=c["stride"], padding=c["padding"], bias=c["bias"])
            elif kind == "batchnorm2d": layer = nn.BatchNorm2d(previous[0])
            elif kind == "maxpool2d": layer = nn.MaxPool2d(kernel_size=c["kernel"], stride=c["stride"], padding=c["padding"])
            elif kind == "avgpool2d": layer = nn.AvgPool2d(kernel_size=c["kernel"], stride=c["stride"], padding=c["padding"])
            elif kind == "globalpool2d": layer = nn.Sequential(nn.AdaptiveAvgPool2d(1), nn.Flatten(1))
            elif kind == "flatten": layer = nn.Flatten(start_dim=1)
            elif kind == "patchembedding": layer = PatchEmbedding(previous[0], c["width"], c["patch"])
            else: raise Invalid("Unsupported architecture layer.")
            self.layers[f"layer_{i}"] = layer
        for i, node in enumerate(order):
            if node["kind"] == "linear" and node["config"]["tieEmbedding"]:
                self.layers[f"layer_{i}"].weight = self.layers[embedding_key].weight

    def forward(self, x):
        for layer in self.layers.values():
            x = layer(x)
        return x


def parse_json(value):
    def unique(pairs):
        result = {}
        for key, item in pairs:
            require(key not in result, "JSON contains duplicate fields.")
            result[key] = item
        return result
    return json.loads(value, object_pairs_hook=unique,
                      parse_constant=lambda _: (_ for _ in ()).throw(Invalid("JSON numbers must be finite.")))


def validate_state(state, expected):
    require(type(state) in (dict, OrderedDict) and 0 < len(state) <= MAX_TENSORS,
            "Checkpoint must be a plain tensor state_dict, not a model or training bundle.")
    require(set(state) == set(expected), "Checkpoint keys do not match this architecture's layers.layer_N state_dict.")
    total = 0
    for key, value in state.items():
        require(isinstance(value, torch.Tensor) and value.layout == torch.strided and not value.is_quantized,
                "Checkpoint must contain only dense, unquantized tensors.")
        require(tuple(value.shape) == tuple(expected[key].shape), "Checkpoint tensor shapes do not match the saved architecture.")
        require(value.dtype in (torch.float16, torch.bfloat16, torch.float32, torch.float64)
                if expected[key].is_floating_point() else value.dtype == expected[key].dtype,
                "Checkpoint tensor dtypes are incompatible with the architecture.")
        total += value.numel()*value.element_size()
        require(total <= MAX_WEIGHTS, "Checkpoint tensor data exceeds 64 MiB.")


def read_weights(path, format_name, expected):
    require(type(path) is str and len(path) <= 4096, "Checkpoint cache path is missing.")
    flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_NONBLOCK", 0)
    try:
        fd = os.open(path, flags)
    except OSError:
        raise Invalid("Cannot open the private checkpoint cache file.")
    with os.fdopen(fd, "rb") as stream:
        info = os.fstat(stream.fileno())
        require(stat.S_ISREG(info.st_mode) and 0 < info.st_size <= MAX_WEIGHTS,
                "Checkpoint must be a regular file of at most 64 MiB.")
        if format_name == "pt":
            require(zipfile.is_zipfile(stream), "Use a modern torch.save tensor state_dict (.pt/.pth), or safetensors.")
            stream.seek(0)
            with zipfile.ZipFile(stream) as archive:
                items = archive.infolist()
                require(len(items) <= MAX_TENSORS+20 and len({item.filename for item in items}) == len(items), "Checkpoint archive has too many or duplicate records.")
                require(all(item.compress_type == zipfile.ZIP_STORED for item in items) and
                        sum(item.file_size for item in items) <= MAX_WEIGHTS, "Checkpoint archive exceeds the tensor allocation limit.")
                require(all(item.file_size <= MAX_HEADER for item in items if not "/data/" in item.filename), "Checkpoint metadata exceeds 1 MiB.")
            # FakeTensorMode reads tensor metadata without allocating tensor storage.
            from torch._subclasses.fake_tensor import FakeTensorMode
            stream.seek(0)
            try:
                with FakeTensorMode():
                    metadata = torch.load(stream, map_location="cpu", weights_only=True)
                validate_state(metadata, expected)
                stream.seek(0)
                state = torch.load(stream, map_location="cpu", weights_only=True)
            except Invalid:
                raise
            except Exception:
                raise Invalid("Cannot read checkpoint safely. Export only a tensor state_dict with torch.save; executable pickle globals are rejected.")
        elif format_name == "safetensors":
            raw = stream.read(8)
            require(len(raw) == 8, "Safetensors header is missing.")
            size = struct.unpack("<Q", raw)[0]
            require(0 < size <= MAX_HEADER and size+8 <= info.st_size, "Safetensors header must be at most 1 MiB and fit the file.")
            try:
                header = parse_json(stream.read(size))
            except (ValueError, UnicodeError):
                raise Invalid("Invalid safetensors JSON header.")
            require(type(header) is dict and len(header) <= MAX_TENSORS+1, "Invalid safetensors tensor metadata.")
            tensors = {key: value for key, value in header.items() if key != "__metadata__"}
            require(set(tensors) == set(expected), "Checkpoint keys do not match this architecture's layers.layer_N state_dict.")
            total = 0
            dtype_sizes = {"F16": 2, "BF16": 2, "F32": 4, "F64": 8, "I64": 8}
            ranges = []
            for key, value in tensors.items():
                object_keys(value, ("dtype", "shape", "data_offsets"), "safetensors tensor")
                shape, dtype, offsets = value.get("shape"), value.get("dtype"), value.get("data_offsets")
                require(type(shape) is list and all(type(x) is int and x >= 0 for x in shape) and tuple(shape) == tuple(expected[key].shape), "Checkpoint tensor shapes do not match the saved architecture.")
                require(type(dtype) is str and dtype in dtype_sizes, "Unsupported safetensors dtype.")
                require(dtype in ("F16", "BF16", "F32", "F64") if expected[key].is_floating_point() else dtype == "I64", "Checkpoint tensor dtypes are incompatible with the architecture.")
                count = math.prod(shape)*dtype_sizes[dtype]
                require(type(offsets) is list and len(offsets) == 2 and all(type(x) is int for x in offsets) and 0 <= offsets[0] <= offsets[1] <= info.st_size-size-8 and offsets[1]-offsets[0] == count, "Invalid safetensors data offsets.")
                ranges.append(offsets)
                total += count
                require(total <= MAX_WEIGHTS, "Checkpoint tensor data exceeds 64 MiB.")
            end = 0
            for start, finish in sorted(ranges):
                require(start == end, "Safetensors data ranges must not overlap or contain gaps.")
                end = finish
            require(end == info.st_size-size-8, "Safetensors contains unexpected trailing data.")
            # Read the already-open private file, avoiding path substitution between validation/load.
            from safetensors.torch import load
            stream.seek(0)
            try:
                state = load(stream.read(MAX_WEIGHTS+1))
            except Exception:
                raise Invalid("Cannot read safetensors checkpoint; export a valid tensor state_dict.")
        else:
            raise Invalid("Choose pt (.pt/.pth state_dict) or safetensors checkpoint format.")
    validate_state(state, expected)
    for tensor in state.values():
        require(bool(torch.isfinite(tensor).all()), "Checkpoint contains non-finite tensor values.")
    return state


def prepare_selection(selection, order, shapes):
    require(type(selection) is dict and type(selection.get("config")) is dict, "Save Data preparation before importing a checkpoint.")
    c = dict(selection["config"])
    mode, purpose = c.get("mode"), c.get("purpose")
    require(mode in ("features", "text", "image") and purpose in ("classification", "regression", "text-generation"), "Unsupported saved Data input mode or purpose.")
    source = order[0]["config"]
    require(source["mode"] == {"text": "tokens", "features": "features", "image": "image"}[mode], "Saved Data input mode does not match the architecture.")
    features = c.get("inputColumns")
    require(type(features) is list and 1 <= len(features) <= 65536 and all(type(x) is str and 0 < len(x) <= 256 for x in features) and len(set(features)) == len(features), "Saved input column names are invalid.")
    classes = selection.get("classes", []) or []
    require(type(classes) is list and len(classes) <= 4096 and all(type(x) is str and 0 < len(x.encode("utf-8")) <= 256 for x in classes) and len(set(classes)) == len(classes), "Saved class labels are invalid or exceed limits.")
    schema = dict(mode=mode, features=features if mode == "features" else [], sequenceLength=0,
                  imageWidth=0, imageHeight=0, imageChannels=0, classes=classes)
    stats = {}
    if mode == "features":
        require(len(features) == source["features"], "Saved feature count does not match the architecture Input.")
        require(c.get("scale", "none") in ("none", "standard"), "Unsupported saved numeric scaling.")
        if c.get("scale") == "standard":
            statistics = selection.get("statistics")
            require(type(statistics) is list and len(statistics) == len(features), "Saved training normalization statistics are missing.")
            for item in statistics:
                require(type(item) is dict and type(item.get("column")) is str and item["column"] in features and item["column"] not in stats and number(item.get("mean")) and number(item.get("std")) and item["std"] >= 0, "Invalid saved training normalization statistics.")
                stats[item["column"]] = (item["mean"], item["std"] or 1)
    elif mode == "text":
        length = integer(c.get("sequenceLength"), 3, 8192, "Saved text sequence length")
        require(length == source["sequence"], "Saved text sequence length must match the architecture Input.")
        require(len(features) == 1, "Text preparation requires one input column.")
        embedding = next((node for node in order if node["kind"] == "embedding"), None)
        require(embedding and embedding["config"]["vocab"] == 259, "Saved UTF-8 byte preparation requires an Embedding vocabulary of 259.")
        schema["sequenceLength"] = length
    else:
        require(len(features) == 1, "Image preparation requires one input column.")
        width = integer(c.get("imageWidth"), 1, 4096, "Image width")
        height = integer(c.get("imageHeight"), 1, 4096, "Image height")
        channels = c.get("imageChannels")
        require(type(channels) is int and channels in (1, 3, 4), "Image channels must be 1, 3, or 4.")
        require((width, height, channels) == (source["width"], source["height"], source["channels"]), "Saved image dimensions must match the architecture Input.")
        require(width*height <= MAX_IMAGE_PIXELS, "Saved images exceed the 4 million pixel limit.")
        schema.update(imageWidth=width, imageHeight=height, imageChannels=channels)
    if purpose == "text-generation":
        require(mode == "text" and shapes[-1] == [schema["sequenceLength"], 259], "Text generation needs one 259-token logit vector per input token.")
        compatible = all(node["config"].get("causal", True) for node in order if node["kind"] in ("attention", "transformer"))
    elif purpose == "classification":
        require(classes and shapes[-1] == [len(classes)], "Classifier output width must match the saved class labels.")
        compatible = False
    else:
        require(shapes[-1] == [1], "Regression output must be one numeric value.")
        compatible = False
    return c, schema, stats, compatible


class Worker:
    def __init__(self):
        self.model = None
        self.closed = False
        torch.set_num_threads(1)

    def handle(self, request):
        try:
            require(type(request) is dict, "Request must be a JSON object.")
            require(not self.closed, "Inference worker is closed; load a new worker.")
            op = request.get("op")
            if op == "close":
                self.model, self.closed = None, True
                return {"ok": True}
            if op == "shape":
                return self.shape(request)
            if op == "load":
                return self.load(request)
            if op == "predict":
                return self.predict(request)
            raise Invalid("Choose load, predict, or close.")
        except Invalid as error:
            return {"ok": False, "error": str(error)}
        except Exception:
            return {"ok": False, "error": "Local inference failed. Check the checkpoint, saved architecture, Data preparation, and input limits."}

    def shape(self, request):
        order, shapes = analyze(request.get("graph"))
        with torch.random.fork_rng():
            torch.manual_seed(7)
            model = Model(order, shapes).eval()
            dtype = torch.long if order[0]["config"]["mode"] == "tokens" else torch.float32
            with torch.inference_mode():
                result = model(torch.zeros((1, *shapes[0]), dtype=dtype))
        require(list(result.shape) == [1, *shapes[-1]] and bool(torch.isfinite(result).all()),
                "Architecture produced invalid or non-finite outputs.")
        return dict(ok=True, inputShape=[1, *shapes[0]], outputShape=list(result.shape),
                    parameterCount=sum(p.numel() for p in model.parameters()), device="cpu", untrained=True)

    def load(self, request):
        self.model = None  # A failed replacement must never keep an old checkpoint usable.
        prompt_format = request.get("promptFormat") or "chat-v1"
        require(prompt_format in ("chat-v1", "prompt-response-v1", "text-v1"), "Checkpoint prompt format is unsupported.")

        order, shapes = analyze(request.get("graph"))
        config, schema, statistics, compatible = prepare_selection(request.get("selection"), order, shapes)
        with torch.device("meta"):
            model = Model(order, shapes)
        expected = model.state_dict()
        require(sum(t.numel()*t.element_size() for t in expected.values()) <= MAX_WEIGHTS,
                "Graph tensor data exceeds 64 MiB.")
        state = read_weights(request.get("weightsPath"), request.get("format"), expected)
        # Check tied aliases before copy: contradictory duplicate values are not a valid tied model.
        embedding = next((i for i, node in enumerate(order) if node["kind"] == "embedding"), None)
        for i, node in enumerate(order):
            if node["kind"] == "linear" and node["config"]["tieEmbedding"]:
                require(torch.equal(state[f"layers.layer_{i}.weight"], state[f"layers.layer_{embedding}.weight"]), "Checkpoint has contradictory tied embedding weights.")
        model.to_empty(device="cpu")
        model.load_state_dict(state, strict=True)
        model.eval()
        dtype = torch.long if config["mode"] == "text" else torch.float32
        with torch.inference_mode():
            result = model(torch.zeros((1, *shapes[0]), dtype=dtype))
        require(list(result.shape) == [1, *shapes[-1]] and bool(torch.isfinite(result).all()),
                "Loaded checkpoint produced invalid or non-finite predictions.")
        self.model, self.config, self.schema, self.statistics = model, config, schema, statistics
        self.prompt_format = prompt_format
        return dict(ok=True, input=schema, chatCompatible=compatible, device="cpu")

    def numeric_input(self, request):
        features = request.get("features")
        require(type(features) is dict and set(features) == set(self.schema["features"]), "Provide exactly the saved numeric feature columns.")
        values = []
        for column in self.schema["features"]:
            value = features[column]
            require(number(value), "Every feature must be a finite number.")
            if column in self.statistics:
                mean, divisor = self.statistics[column]
                difference = value-mean
                value = difference/divisor if math.isfinite(difference) else value/divisor-mean/divisor
            require(number(value) and abs(value) <= torch.finfo(torch.float32).max, "Numeric feature overflows after saved normalization.")
            values.append(value)
        return torch.tensor([values], dtype=torch.float32)

    def image_input(self, request):
        encoded = request.get("imageBase64")
        require(type(encoded) is str and 0 < len(encoded) <= (MAX_IMAGE_BYTES+2)//3*4, "Provide an image encoded as base64, at most 8 MiB decoded.")
        try:
            data = base64.b64decode(encoded, validate=True)
        except (ValueError, binascii.Error):
            raise Invalid("Image is not valid base64.")
        require(len(data) <= MAX_IMAGE_BYTES, "Image exceeds the 8 MiB file limit.")
        from PIL import Image
        Image.MAX_IMAGE_PIXELS = MAX_IMAGE_PIXELS
        try:
            with warnings.catch_warnings():
                warnings.simplefilter("error", Image.DecompressionBombWarning)
                with Image.open(io.BytesIO(data), formats=("PNG", "JPEG", "WEBP", "BMP")) as image:
                    require(image.width*image.height <= MAX_IMAGE_PIXELS, "Source image exceeds the 4 million pixel limit.")
                    require(getattr(image, "n_frames", 1) == 1, "Use a single-frame image.")
                    channels = self.schema["imageChannels"]
                    converted = image.convert({1: "L", 3: "RGB", 4: "RGBA"}[channels])
                    converted = converted.resize((self.schema["imageWidth"], self.schema["imageHeight"]), Image.Resampling.BILINEAR)
                    values = torch.frombuffer(bytearray(converted.tobytes()), dtype=torch.uint8)
                    return values.reshape(self.schema["imageHeight"], self.schema["imageWidth"], channels).permute(2, 0, 1).unsqueeze(0).to(torch.float32).div_(255)
        except Invalid:
            raise
        except Exception:
            raise Invalid("Cannot decode image safely. Use a single-frame PNG, JPEG, WEBP, or BMP within the pixel limit.")

    def text_input(self, request, generation):
        prompt = request.get("prompt")
        require(type(prompt) is str and 0 < len(prompt) <= 8192, "Provide a nonempty text prompt within the saved context length.")
        try:
            data = prompt.encode("utf-8")
        except UnicodeError:
            raise Invalid("Prompt must contain valid UTF-8 text.")
        ids = [1] + [value+3 for value in data]
        length = self.schema["sequenceLength"]
        require(len(ids) < length if generation else len(ids)+1 <= length,
                f"Prompt exceeds the saved {length}-token UTF-8 byte context; shorten the conversation.")
        if generation:
            if self.prompt_format == "chat-v1":
                require(prompt.startswith("User:") and prompt.endswith("Assistant:"), "Text generation expects User:/Assistant: framed messages.")
        else:
            ids += [2] + [0]*(length-len(ids)-1)
        return ids

    def forward(self, inputs):
        with torch.inference_mode():
            result = self.model(inputs)
        require(bool(torch.isfinite(result).all()), "Checkpoint produced non-finite predictions for this input.")
        return result

    def predict(self, request):
        require(self.model is not None, "Load a valid checkpoint before predicting.")
        mode, purpose = self.config["mode"], self.config["purpose"]
        if purpose == "text-generation":
            limit = integer(request.get("maxNewTokens", 64), 1, MAX_NEW_TOKENS, "maxNewTokens")
            temperature = request.get("temperature", 0)
            require(number(temperature) and 0 <= temperature <= 2, "Temperature must be between 0 and 2.")
            ids, generated, finished = self.text_input(request, True), [], False
            capacity = self.schema["sequenceLength"]-len(ids)
            for _ in range(min(limit, capacity)):
                scores = self.forward(torch.tensor([ids], dtype=torch.long))[0, -1].clone()
                scores[0] = scores[1] = -torch.inf  # Padding/BOS are not response bytes.
                if temperature == 0:
                    token = int(scores.argmax())
                else:
                    probabilities = torch.softmax(scores/temperature, dim=-1)
                    require(bool(torch.isfinite(probabilities).all()), "Sampling probabilities are non-finite; use a higher temperature.")
                    token = int(torch.multinomial(probabilities, 1))
                if token == 2:
                    finished = True
                    break
                require(3 <= token <= 258, "Checkpoint emitted a token outside the UTF-8 byte vocabulary.")
                generated.append(token-3)
                ids.append(token)
            return dict(ok=True, text=bytes(generated).decode("utf-8", errors="replace"), device="cpu", truncated=not finished)
        if mode == "features": inputs = self.numeric_input(request)
        elif mode == "image": inputs = self.image_input(request)
        else: inputs = torch.tensor([self.text_input(request, False)], dtype=torch.long)
        result = self.forward(inputs)[0]
        if purpose == "regression":
            return dict(ok=True, value=float(result[0]), device="cpu")
        probabilities = torch.softmax(result, dim=-1)
        labels = self.schema["classes"]
        return dict(ok=True, label=labels[int(probabilities.argmax())],
                    scores=[dict(label=label, value=float(probabilities[i])) for i, label in enumerate(labels)], device="cpu")


def main():
    worker = Worker()
    while True:
        line = sys.stdin.buffer.readline(MAX_LINE+1)
        if not line:
            return
        if len(line) > MAX_LINE:
            response = dict(ok=False, error="Inference request exceeds the 12 MiB IPC limit.")
            # Do not accumulate an unbounded attacker-controlled line.
            while line and not line.endswith(b"\n"):
                line = sys.stdin.buffer.readline(MAX_LINE+1)
        else:
            try:
                response = worker.handle(parse_json(line))
            except Exception:
                response = dict(ok=False, error="Request must be one valid JSON object per line.")
        encoded = json.dumps(response, allow_nan=False, separators=(",", ":"))
        if len(encoded.encode("utf-8")) > MAX_LINE:
            encoded = '{"ok":false,"error":"Inference response exceeds the IPC limit."}'
        sys.stdout.write(encoded+"\n")
        sys.stdout.flush()
        if worker.closed:
            return


if __name__ == "__main__":
    main()
