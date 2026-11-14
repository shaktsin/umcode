"""Real CPU training, held-out metrics, and bounded JSON-line input tests."""
import copy
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

import torch
from torch import nn

from test_runner import graph, selection

HERE = Path(__file__).resolve().parent


class TrainTests(unittest.TestCase):
    def setUp(self):
        torch.set_num_threads(1)
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)

    def trainer(self):
        self.assertTrue((HERE / "train.py").exists(), "trusted trainer has not been implemented")
        spec = importlib.util.spec_from_file_location("trainer", HERE / "train.py")
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        return module

    def request(self, train, validation=(), test=(), **overrides):
        paths = {}
        for name, rows in (("train", train), ("validation", validation), ("test", test)):
            path = self.root / (name + ".jsonl")
            path.write_text("".join(json.dumps(row) + "\n" for row in rows))
            paths[name] = str(path)
        return {"graph": graph([("input", {"mode": "features", "features": 1}),
            ("linear", {"outFeatures": 1}), ("output", {})]),
            "selection": selection(), "config": {"epochs": 200, "batchSize": 8,
            "learningRate": .04, "seed": 17}, "splitPaths": paths,
            "weightsPath": str(self.root / "weights.pt"), **overrides}

    def test_regression_learns_and_reports_independent_heldout_metrics(self):
        rows = [{"inputs": [x / 5], "target": 3 * x / 5 + 2} for x in range(-10, 11)]
        request = self.request(rows, [{"inputs": [1.5], "target": 6.5}],
            [{"inputs": [0], "target": 12}, {"inputs": [1], "target": 15}])
        events = []
        result = self.trainer().train(request, events.append)
        self.assertEqual(result["type"], "complete")
        self.assertEqual(len(events), 200)
        self.assertEqual(events[-1]["epoch"], 200)
        self.assertLess(result["train"]["rmse"], .05)
        self.assertLess(result["validation"]["mae"], .05)
        self.assertAlmostEqual(result["test"]["mae"], 10, delta=.05)
        self.assertEqual(result["test"]["samples"], 2)
        self.assertLess(result["test"]["r2"], 0)
        self.assertEqual(result["parameterCount"], 2)
        state = torch.load(request["weightsPath"], weights_only=True)
        self.assertAlmostEqual(float(state["layers.layer_1.weight"][0, 0]), 3, delta=.05)
        self.assertTrue(all(isinstance(value, torch.Tensor) for value in state.values()))

    def test_classification_learns_accuracy_and_macro_f1(self):
        saved = selection(purpose="classification")
        saved["classes"] = ["negative", "positive"]
        g = graph([("input", {"mode": "features", "features": 1}),
            ("linear", {"outFeatures": 2}), ("output", {})])
        rows = [{"inputs": [x], "target": int(x > 0)} for x in [-3, -2, -1, 1, 2, 3]]
        request = self.request(rows, rows, rows, graph=g, selection=saved)
        result = self.trainer().train(request, lambda _: None)
        self.assertEqual(result["test"]["accuracy"], 1)
        self.assertEqual(result["test"]["macroF1"], 1)
        self.assertLess(result["test"]["loss"], .02)

    def test_text_classification_trains_saved_byte_inputs(self):
        saved = selection(mode="text", purpose="classification", sequenceLength=4)
        saved["classes"] = ["a", "b"]
        g = graph([("input", {"mode": "tokens", "sequence": 4}),
            ("embedding", {"vocab": 259, "width": 8}), ("meanpool", {}),
            ("linear", {"outFeatures": 2}), ("output", {})])
        rows = [{"inputIds": [1, 100 + x, 2, 0], "attentionMask": [1, 1, 1, 0], "target": x}
            for x in [0, 1] * 4]
        result = self.trainer().train(self.request(rows, test=rows, graph=g, selection=saved), lambda _: None)
        self.assertEqual(result["test"]["accuracy"], 1)

    def test_generation_shifts_logits_and_weights_metrics_by_unmasked_tokens(self):
        module = self.trainer()
        # Token 1 predicts 4, token 4 predicts 5, token 5 predicts EOS.
        model = nn.Embedding(259, 259)
        with torch.no_grad():
            model.weight.fill_(-10)
            model.weight[1, 4] = model.weight[4, 5] = model.weight[5, 2] = 10
        inputs = torch.tensor([[1, 4, 5, 2], [1, 4, 5, 2]])
        targets = torch.tensor([[-100, 4, 5, 2], [-100, -100, -100, 4]])
        metrics = module.evaluate(model, (inputs, targets), "text-generation", 1, 0)
        self.assertAlmostEqual(metrics["tokenAccuracy"], .75)
        self.assertAlmostEqual(metrics["loss"], 5, places=4)
        self.assertAlmostEqual(metrics["perplexity"], 148.413159, places=2)

    def test_generation_training_learns_next_tokens_and_saves_loadable_weights(self):
        saved = selection(mode="text", purpose="text-generation", sequenceLength=4)
        g = graph([("input", {"mode": "tokens", "sequence": 4}),
            ("embedding", {"vocab": 259, "width": 8}),
            ("linear", {"outFeatures": 259}), ("output", {})])
        rows = [{"inputIds": [1, 100, 2, 0], "attentionMask": [1, 1, 1, 0],
            "labels": [-100, 100, 2, -100]}] * 8
        request = self.request(rows, rows, rows, graph=g, selection=saved,
            config={"epochs": 30, "batchSize": 4, "learningRate": .05, "seed": 17})
        result = self.trainer().train(request, lambda _: None)
        self.assertEqual(result["test"]["tokenAccuracy"], 1)
        self.assertLess(result["test"]["perplexity"], 1.05)
        from runner import Worker
        loaded = Worker().handle({"op": "load", "graph": g, "selection": saved,
            "weightsPath": request["weightsPath"], "format": "pt"})
        self.assertTrue(loaded["ok"], loaded)

    def test_raw_question_response_training_predicts_with_the_same_separator(self):
        # This is the actual Data-tab recipe: raw question, newline, response.
        source, answer, length = "Q?", "Yes.", 32
        raw = (source + "\n" + answer).encode()
        ids = [1] + [b+3 for b in raw] + [2]
        labels = [-100 if i <= len(source)+1 else token for i, token in enumerate(ids)]
        row = {"inputIds": ids+[0]*(length-len(ids)),
               "attentionMask": [1]*len(ids)+[0]*(length-len(ids)),
               "labels": labels+[-100]*(length-len(ids))}
        g = graph([("input", {"mode":"tokens", "sequence":length}),
            ("embedding", {"vocab":259, "width":16}),
            ("linear", {"outFeatures":259}), ("output", {})])
        saved = selection(mode="text", purpose="text-generation", sequenceLength=length, targetColumn="response")
        request = self.request([row]*4, graph=g, selection=saved,
            config={"epochs":80,"batchSize":4,"learningRate":.05,"seed":17})
        self.trainer().train(request, lambda _: None)
        from runner import Worker
        worker = Worker()
        loaded = worker.handle({"op":"load", "graph":g,"selection":saved,
            "weightsPath":request["weightsPath"],"format":"pt","promptFormat":"prompt-response-v1"})
        self.assertTrue(loaded["ok"],loaded)
        result = worker.handle({"op":"predict","prompt":source+"\n","maxNewTokens":16,"temperature":0})
        self.assertTrue(result["ok"],result)
        self.assertEqual(result["text"],answer)

    def test_regression_metrics_weight_final_short_batch_and_constant_r2_is_unavailable(self):
        model = nn.Linear(1, 1, bias=False)
        with torch.no_grad():
            model.weight.fill_(0)
        metrics = self.trainer().evaluate(model,
            (torch.tensor([[0.], [0.], [0.]]), torch.tensor([1., 1., 4.])), "regression", 2, 0)
        self.assertEqual(metrics["loss"], 6)
        self.assertEqual(metrics["mae"], 2)
        self.assertAlmostEqual(metrics["rmse"], 6**.5)
        self.assertEqual(metrics["r2"], -2)
        constant = self.trainer().evaluate(model,
            (torch.tensor([[0.], [0.]]), torch.tensor([1., 1.])), "regression", 2, 0)
        self.assertIsNone(constant["r2"])

    def test_transformer_training_bounds_include_repeated_intermediate_activations(self):
        g = graph([("input", {"mode": "tokens", "sequence": 64}),
            ("embedding", {"vocab": 259, "width": 64}),
            ("transformer", {"width": 64, "heads": 4, "expansion": 16, "repeat": 48}),
            ("linear", {"outFeatures": 259}), ("output", {})])
        request = self.request([], graph=g,
            selection=selection(mode="text", purpose="text-generation", sequenceLength=64))
        with self.assertRaisesRegex(ValueError, "batch|activation"):
            self.trainer().train(request, lambda _: None)

    def test_parent_memory_preflight_can_allow_batch_above_old_fixed_activation_cap(self):
        g = graph([("input", {"mode": "tokens", "sequence": 512}),
            ("embedding", {"vocab": 259, "width": 32}),
            ("position", {"length": 512}),
            ("transformer", {"width": 32, "heads": 4, "expansion": 2, "repeat": 1, "causal": False}),
            ("meanpool", {}), ("linear", {"outFeatures": 2}), ("output", {})])
        saved = selection(mode="text", purpose="classification", sequenceLength=512)
        saved["classes"] = ["a", "b"]
        request = self.request([], graph=g, selection=saved,
            config={"epochs": 1, "batchSize": 16, "learningRate": .003, "seed": 17}, maxBatchSize=16)
        with self.assertRaisesRegex(ValueError, "training split is empty"):
            self.trainer().train(request, lambda _: None)

    def test_oversized_prepared_file_is_rejected_before_reading(self):
        request = self.request([{"inputs": [1], "target": 1}])
        with Path(request["splitPaths"]["train"]).open("wb") as stream:
            stream.truncate(256*1024*1024+1)
        with self.assertRaisesRegex(ValueError, "256 MiB"):
            self.trainer().train(request, lambda _: None)

    def test_checkpoint_tensor_count_limit_is_enforced_before_loading_data(self):
        # 35*48 transformer blocks have 20160 state tensors, exceeding the
        # inference loader's limit despite having relatively few parameters.
        g = graph([("input", {"mode": "tokens", "sequence": 3}),
            ("embedding", {"vocab": 259, "width": 1})] +
            [("transformer", {"width": 1, "heads": 1, "repeat": 48})] * 35 +
            [("linear", {"outFeatures": 259}), ("output", {})])
        request = self.request([], graph=g,
            selection=selection(mode="text", purpose="text-generation", sequenceLength=3))
        with self.assertRaisesRegex(ValueError, "tensor|layer"):
            self.trainer().train(request, lambda _: None)

    def test_empty_evaluation_is_null(self):
        result = self.trainer().train(self.request([{"inputs": [1], "target": 3}]), lambda _: None)
        self.assertIsNone(result["validation"])
        self.assertIsNone(result["test"])

    def test_invalid_config_and_model_never_write_weights(self):
        module = self.trainer()
        request = self.request([{"inputs": [1], "target": 3}])
        for field, value in [("epochs", 0), ("epochs", 1001), ("batchSize", True),
            ("batchSize", 257), ("learningRate", float("nan")), ("learningRate", 0),
            ("seed", -1), ("seed", 2147483648)]:
            invalid = copy.deepcopy(request)
            invalid["config"][field] = value
            with self.subTest(field=field, value=value), self.assertRaises(ValueError):
                module.train(invalid, lambda _: None)
            self.assertFalse(Path(request["weightsPath"]).exists())
        request["graph"]["nodes"][1]["config"]["outFeatures"] = 2
        with self.assertRaises(ValueError):
            module.train(request, lambda _: None)

    def test_malformed_rows_empty_training_and_resource_bounds_are_rejected(self):
        module = self.trainer()
        for rows in [[], [{"inputs": [1, 2], "target": 3}],
            [{"inputs": [True], "target": 3}], [{"inputs": [1e100], "target": 3}],
            [{"inputs": [1], "target": "bad"}]]:
            with self.subTest(rows=rows), self.assertRaises(ValueError):
                module.train(self.request(rows), lambda _: None)
        g = graph([("input", {"mode": "tokens", "sequence": 8192}),
            ("embedding", {"vocab": 259, "width": 16}),
            ("linear", {"outFeatures": 259}), ("output", {})])
        request = self.request([], graph=g,
            selection=selection(mode="text", purpose="text-generation", sequenceLength=8192))
        with self.assertRaisesRegex(ValueError, "batch|activation"):
            module.train(request, lambda _: None)

    def test_cli_emits_one_safe_error_and_no_checkpoint(self):
        self.trainer()
        request = self.request([{"inputs": [1], "target": 3}])
        request["config"]["epochs"] = 0
        process = subprocess.run([sys.executable, str(HERE / "train.py")],
            input=json.dumps(request) + "\n", capture_output=True, text=True, timeout=15)
        self.assertEqual(process.returncode, 0, process.stderr)
        lines = [json.loads(line) for line in process.stdout.splitlines()]
        self.assertEqual(len(lines), 1)
        self.assertEqual(lines[0]["type"], "error")
        self.assertFalse(Path(request["weightsPath"]).exists())


if __name__ == "__main__":
    unittest.main()
