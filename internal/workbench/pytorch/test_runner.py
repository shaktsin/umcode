"""Behavioral tests: real trained weights and the JSON-line trust boundary."""
import base64
import copy
import importlib.util
import io
import json
import os
from pathlib import Path
import struct
import subprocess
import sys
import tempfile
import unittest

import torch
from torch import nn
from PIL import Image
from safetensors.torch import save_file

HERE = Path(__file__).resolve().parent


def graph(entries):
    return {"version": 1, "format": "umcode.layers.v1", "nodes": [
        {"id": f"node{i}", "kind": kind, "position": {"x": 0, "y": i}, "config": config}
        for i, (kind, config) in enumerate(entries)], "edges": [
            {"id": f"edge{i}", "source": f"node{i-1}", "target": f"node{i}"}
            for i in range(1, len(entries))]}


def selection(mode="features", purpose="regression", **config):
    return {"version": "fixture", "datasetId": "fixture", "config": {
        "mode": mode, "purpose": purpose, "inputColumns": ["x"], "scale": "none",
        **config}, "classes": [], "statistics": []}


class RunnerTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        torch.set_num_threads(1)
        cls.temp = tempfile.TemporaryDirectory()
        cls.root = Path(cls.temp.name)
        # Removing weight loading or preprocessing must break the prediction test.
        cls.numeric_graph = graph([("input", {"mode": "features", "features": 1}),
                                   ("linear", {"outFeatures": 1}), ("output", {})])
        model = nn.Module()
        model.layers = nn.ModuleDict({"layer_1": nn.Linear(1, 1)})
        torch.manual_seed(7)
        x = torch.linspace(-2, 2, 40).reshape(-1, 1)
        optimizer = torch.optim.SGD(model.parameters(), lr=.1)
        for _ in range(160):
            optimizer.zero_grad()
            loss = ((model.layers["layer_1"](x) - (3*x+2))**2).mean()
            loss.backward()
            optimizer.step()
        cls.weights = cls.root / "trained.pt"
        torch.save(model.state_dict(), cls.weights)
        cls.safe_weights = cls.root / "trained.safetensors"
        save_file(model.state_dict(), str(cls.safe_weights))

    @classmethod
    def tearDownClass(cls):
        cls.temp.cleanup()

    def test_architecture_runs_without_weights_and_never_loads_a_checkpoint(self):
        worker = self.worker()
        result = worker.handle({"op": "shape", "graph": self.numeric_graph})
        self.assertTrue(result["ok"], result)
        self.assertEqual(result["inputShape"], [1, 1])
        self.assertEqual(result["outputShape"], [1, 1])
        self.assertEqual(result["parameterCount"], 2)
        self.assertIsNone(worker.model)
        self.assertFalse(worker.handle({"op": "predict", "features": {"x": 1}})["ok"])

    def worker(self):
        self.assertTrue((HERE / "runner.py").exists(), "trusted Python runner has not been implemented")
        spec = importlib.util.spec_from_file_location("workbench_runner", HERE / "runner.py")
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        return module.Worker()

    def load(self, worker, **overrides):
        return worker.handle({"op": "load", "graph": self.numeric_graph,
            "selection": selection(), "weightsPath": str(self.weights), "format": "pt", **overrides})

    def test_trained_state_dict_prediction_uses_saved_normalization(self):
        worker = self.worker()
        saved = selection(scale="standard")
        saved["statistics"] = [{"column": "x", "mean": 10, "std": 2}]
        loaded = self.load(worker, selection=saved)
        self.assertTrue(loaded["ok"], loaded)
        self.assertEqual(loaded["input"]["features"], ["x"])
        self.assertFalse(loaded["chatCompatible"])
        answer = worker.handle({"op": "predict", "features": {"x": 14}})
        self.assertTrue(answer["ok"], answer)
        self.assertAlmostEqual(answer["value"], 8.0, places=4)
        self.assertEqual(answer["device"], "cpu")

    def test_safetensors_predicts_the_same_trained_function(self):
        worker = self.worker()
        loaded = self.load(worker, weightsPath=str(self.safe_weights), format="safetensors")
        self.assertTrue(loaded["ok"], loaded)
        answer = worker.handle({"op": "predict", "features": {"x": -1}})
        self.assertAlmostEqual(answer["value"], -1.0, places=4)

    def test_classifier_returns_saved_class_labels_and_real_scores(self):
        worker = self.worker()
        g = graph([("input", {"mode": "features", "features": 1}),
                   ("linear", {"outFeatures": 2}), ("output", {})])
        state = {"layers.layer_1.weight": torch.tensor([[-1.], [1.]]),
                 "layers.layer_1.bias": torch.zeros(2)}
        path = self.root / "classifier.pt"
        torch.save(state, path)
        saved = selection(purpose="classification")
        saved["classes"] = ["cold", "hot"]
        self.assertTrue(self.load(worker, graph=g, selection=saved, weightsPath=str(path))["ok"])
        answer = worker.handle({"op": "predict", "features": {"x": 2}})
        self.assertEqual(answer["label"], "hot")
        self.assertAlmostEqual(answer["scores"][1]["value"], .98201379, places=6)

    def test_graph_and_input_rejections_are_actionable(self):
        worker = self.worker()
        malformed = copy.deepcopy(self.numeric_graph)
        malformed["nodes"][1]["kind"] = "execute_python"
        self.assertFalse(self.load(worker, graph=malformed)["ok"])
        broken = copy.deepcopy(self.numeric_graph)
        broken["edges"] = []
        self.assertFalse(self.load(worker, graph=broken)["ok"])
        self.assertTrue(self.load(worker)["ok"])
        for request in [{"features": {}}, {"features": {"x": float("nan")}},
                        {"features": {"x": 1, "extra": 2}}, {"features": {"x": True}}]:
            answer = worker.handle({"op": "predict", **request})
            self.assertFalse(answer["ok"], answer)
            self.assertTrue(answer["error"])

    def test_wrong_nonfinite_and_unsafe_weights_do_not_load(self):
        worker = self.worker()
        for i, state in enumerate([{"other": torch.zeros(1)},
            {"layers.layer_1.weight": torch.tensor([[float("inf")]]),
             "layers.layer_1.bias": torch.zeros(1)}, {"state_dict": {"x": torch.zeros(1)}}]):
            path = self.root / f"bad{i}.pt"
            torch.save(state, path)
            result = self.load(worker, weightsPath=str(path))
            self.assertFalse(result["ok"], result)
        marker = self.root / "unsafe-ran"
        class Exploit:
            def __reduce__(self):
                return (os.system, (f"touch {marker}",))
        path = self.root / "unsafe.pt"
        torch.save(Exploit(), path)
        self.assertFalse(self.load(worker, weightsPath=str(path))["ok"])
        self.assertFalse(marker.exists())
        self.assertFalse(worker.handle({"op": "predict", "features": {"x": 1}})["ok"])

    def test_allocation_limits_reject_before_materialization(self):
        worker = self.worker()
        huge = copy.deepcopy(self.numeric_graph)
        huge["nodes"][0]["config"]["features"] = 65536
        huge["nodes"][1]["config"]["outFeatures"] = 65536
        self.assertFalse(self.load(worker, graph=huge)["ok"])
        path = self.root / "oversized-header.safetensors"
        path.write_bytes(struct.pack("<Q", 2**40) + b"{}")
        self.assertFalse(self.load(worker, weightsPath=str(path), format="safetensors")["ok"])
        path = self.root / "huge.pt"
        with path.open("wb") as stream:
            stream.truncate(64*1024*1024+1)
        self.assertFalse(self.load(worker, weightsPath=str(path))["ok"])

    def test_image_pixels_are_decoded_and_scaled(self):
        worker = self.worker()
        g = graph([("input", {"mode": "image", "channels": 1, "height": 2, "width": 2}),
                   ("flatten", {}), ("linear", {"outFeatures": 1}), ("output", {})])
        path = self.root / "image.pt"
        torch.save({"layers.layer_2.weight": torch.ones(1, 4),
                    "layers.layer_2.bias": torch.zeros(1)}, path)
        saved = selection(mode="image", imageWidth=2, imageHeight=2, imageChannels=1)
        self.assertTrue(self.load(worker, graph=g, selection=saved, weightsPath=str(path))["ok"])
        data = io.BytesIO()
        Image.new("L", (2, 2), 255).save(data, format="PNG")
        answer = worker.handle({"op": "predict", "imageBase64": base64.b64encode(data.getvalue()).decode()})
        self.assertAlmostEqual(answer["value"], 4.0, places=5)
        self.assertFalse(worker.handle({"op": "predict", "imageBase64": "not base64"})["ok"])

    def test_json_line_loop_recovers_from_invalid_input_and_closes(self):
        self.worker()
        request = {"op": "load", "graph": self.numeric_graph, "selection": selection(),
                   "weightsPath": str(self.weights), "format": "pt"}
        process = subprocess.run([sys.executable, "-I", str(HERE / "runner.py")],
            input="bad json\n"+json.dumps(request)+"\n"+json.dumps({"op": "predict", "features": {"x": 2}})+
                  "\n"+json.dumps({"op": "close"})+"\n", text=True, capture_output=True, timeout=20)
        self.assertEqual(process.returncode, 0, process.stderr)
        lines = [json.loads(line) for line in process.stdout.splitlines()]
        self.assertEqual(len(lines), 4)
        self.assertFalse(lines[0]["ok"])
        self.assertTrue(lines[1]["ok"], lines[1])
        self.assertAlmostEqual(lines[2]["value"], 8.0, places=4)
        self.assertTrue(lines[3]["ok"])

    def test_trained_text_state_dict_generates_bytes_and_bounds_context(self):
        # This independently trained byte transition model catches fabricated text,
        # wrong byte offsets, BOS/EOS handling, and ignoring generation bounds.
        torch.manual_seed(13)
        model = nn.Sequential(nn.Embedding(259, 16), nn.Linear(16, 259))
        optimizer = torch.optim.Adam(model.parameters(), lr=.04)
        source = torch.tensor([[ord(":")+3, ord("H")+3, ord("i")+3, ord("!")+3]])
        target = torch.tensor([ord("H")+3, ord("i")+3, ord("!")+3, 2])
        for _ in range(60):
            optimizer.zero_grad()
            loss = nn.functional.cross_entropy(model(source).reshape(-1, 259), target)
            loss.backward()
            optimizer.step()
        path = self.root / "trained-text.pt"
        torch.save({"layers.layer_1.weight": model[0].weight,
                    "layers.layer_2.weight": model[1].weight,
                    "layers.layer_2.bias": model[1].bias}, path)
        g = graph([("input", {"mode": "tokens", "sequence": 64}),
                   ("embedding", {"vocab": 259, "width": 16}),
                   ("linear", {"outFeatures": 259}), ("output", {})])
        worker = self.worker()
        loaded = self.load(worker, graph=g, selection=selection(mode="text", purpose="text-generation", sequenceLength=64), weightsPath=str(path))
        self.assertTrue(loaded["ok"], loaded)
        self.assertTrue(loaded["chatCompatible"])
        answer = worker.handle({"op": "predict", "prompt": "User: Hello\nAssistant:", "maxNewTokens": 8, "temperature": 0})
        self.assertEqual(answer.get("text"), "Hi!", answer)
        self.assertFalse(answer["truncated"])
        cut = worker.handle({"op": "predict", "prompt": "User: Hello\nAssistant:", "maxNewTokens": 1})
        self.assertEqual(cut["text"], "H")
        self.assertTrue(cut["truncated"])
        for request in [{"prompt": "Hello"}, {"prompt": "User:"+"x"*80+"\nAssistant:"},
                        {"prompt": "User: Hello\nAssistant:", "maxNewTokens": 257}]:
            self.assertFalse(worker.handle({"op": "predict", **request})["ok"])

    def test_fixture_generator_exports_real_trained_import_assets(self):
        generator = HERE / "generate_fixtures.py"
        self.assertTrue(generator.exists(), "exportable trained fixture generator has not been implemented")
        output = self.root / "exported"
        result = subprocess.run([sys.executable, str(generator), "--output", str(output), "--steps", "180", "--smoke"],
                                capture_output=True, text=True, timeout=60)
        self.assertEqual(result.returncode, 0, result.stderr)
        worker = self.worker()
        loaded = self.load(worker, graph=json.loads((output / "tiny-chat-architecture.json").read_text()),
            selection=json.loads((output / "tiny-chat-selection.json").read_text()),
            weightsPath=str(output / "tiny-chat.pt"))
        self.assertTrue(loaded["ok"], loaded)
        answer = worker.handle({"op": "predict", "prompt": "User: Hello\nAssistant:", "maxNewTokens": 64})
        self.assertEqual(answer.get("text"), "\nHello! I am a tiny local model.", answer)



class ElementLimitTests(unittest.TestCase):
    """The activation element cap is 8M by default and scales only when the launcher passes a memory-derived value."""
    PROBE = (
        "import json,sys;sys.path.insert(0,%r);import runner;"
        "g=json.loads(sys.stdin.read());\n"
        "try:\n runner.analyze(g);print('OK',runner.MAX_ELEMENTS)\n"
        "except runner.Invalid as e:\n print('ERR',runner.MAX_ELEMENTS,e)\n" % str(HERE))

    def probe(self, value):
        env = {k: v for k, v in os.environ.items() if k != "UMCODE_MAX_ELEMENTS"}
        if value is not None:
            env["UMCODE_MAX_ELEMENTS"] = value
        wide = graph([("input", {"mode": "tokens", "sequence": 2000}), ("embedding", {"vocab": 32, "width": 8}),
                      ("attention", {"heads": 4}), ("meanpool", {}), ("linear", {"outFeatures": 2}), ("output", {})])
        done = subprocess.run([sys.executable, "-I", "-c", self.PROBE], input=json.dumps(wide), env=env,
                              capture_output=True, text=True, timeout=60)
        return done.stdout.strip()

    def test_default_keeps_the_eight_million_cap(self):
        self.assertTrue(self.probe(None).startswith("ERR 8000000 Attention exceeds the 8 million"), self.probe(None))

    def test_launcher_value_raises_the_cap(self):
        self.assertEqual(self.probe("32000000"), "OK 32000000")

    def test_bad_or_too_small_values_fall_back_to_default(self):
        for bad in ("abc", "-5", "1000", "7999999", ""):
            self.assertTrue(self.probe(bad).startswith("ERR 8000000"), (bad, self.probe(bad)))

    def test_value_is_clamped_to_the_hard_ceiling(self):
        self.assertTrue(self.probe("99999999999").startswith("OK 128000000") or
                        self.probe("99999999999").startswith("ERR 128000000"))

    def test_message_names_the_active_cap(self):
        out = self.probe("12000000")
        self.assertIn("12 million", out)



class WorkAndParameterLimitTests(unittest.TestCase):
    """Work and parameter caps keep their defaults and rise only through launcher-provided, range-checked values."""
    PROBE = (
        "import json,sys;sys.path.insert(0,%r);import runner;"
        "g=json.loads(sys.stdin.read());\n"
        "try:\n runner.analyze(g);print('OK')\n"
        "except runner.Invalid as e:\n print('ERR',e)\n" % str(HERE))

    def probe(self, graph_nodes, **env_values):
        env = {k: v for k, v in os.environ.items() if not k.startswith("UMCODE_MAX_")}
        env.update(env_values)
        done = subprocess.run([sys.executable, "-I", "-c", self.PROBE], input=json.dumps(graph(graph_nodes)),
                              env=env, capture_output=True, text=True, timeout=60)
        return done.stdout.strip()

    HEAVY = [("input", {"mode": "tokens", "sequence": 512}), ("embedding", {"vocab": 259, "width": 512}),
             ("transformer", {"width": 512, "heads": 8, "repeat": 1, "expansion": 4}), ("meanpool", {}),
             ("linear", {"outFeatures": 2}), ("output", {})]
    BIG = [("input", {"mode": "tokens", "sequence": 8}), ("embedding", {"vocab": 259, "width": 512}),
           ("transformer", {"width": 512, "heads": 8, "repeat": 6, "expansion": 4}), ("meanpool", {}),
           ("linear", {"outFeatures": 2}), ("output", {})]

    def test_defaults_keep_the_conservative_caps(self):
        self.assertIn("operation limit", self.probe(self.HEAVY))
        self.assertIn("16 million parameter", self.probe(self.BIG))

    def test_launcher_values_raise_the_caps(self):
        self.assertEqual(self.probe(self.HEAVY, UMCODE_MAX_WORK="100000000000", UMCODE_MAX_ELEMENTS="32000000"), "OK")
        self.assertEqual(self.probe(self.BIG, UMCODE_MAX_PARAMETERS="256000000"), "OK")

    def test_out_of_range_values_fall_back(self):
        self.assertIn("operation limit", self.probe(self.HEAVY, UMCODE_MAX_WORK="1"))
        self.assertIn("16 million parameter", self.probe(self.BIG, UMCODE_MAX_PARAMETERS="nope"))

    def test_values_are_clamped_to_hard_ceilings(self):
        from_runner = subprocess.run([sys.executable, "-I", "-c",
            "import sys;sys.path.insert(0,%r);import runner;print(runner.MAX_WORK, runner.MAX_PARAMETERS)" % str(HERE)],
            env=dict(os.environ, UMCODE_MAX_WORK="9"*20, UMCODE_MAX_PARAMETERS="9"*20),
            capture_output=True, text=True).stdout.split()
        self.assertEqual(from_runner, ["2000000000000", "512000000"])


if __name__ == "__main__":
    unittest.main()
