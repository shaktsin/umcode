import { createServer } from 'vite';
import { mkdir, writeFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { join } from 'node:path';
const output = process.env.MODEL_ARTIFACT_DIR || '/tmp/umcode-visual-models';
await mkdir(output, { recursive: true });
const server = await createServer({ root: fileURLToPath(new URL('..', import.meta.url)), server: { middlewareMode: true } });
try {
  const { starterGraph, analyzeGraph } = await server.ssrLoadModule('/src/lib/workbench/graph.ts');
  const { generatePython } = await server.ssrLoadModule('/src/lib/workbench/codegen.ts');
  const manifest = {};
  for (const preset of ['text', 'classifier', 'cnn', 'vit', 'mlp', 'cnn_odd', 'cnn_avg_flatten']) {
    const graph = starterGraph(preset.startsWith('cnn_') ? 'cnn' : preset);
    if (preset === 'cnn_odd') {
      Object.assign(graph.nodes[0].config, { height: 31, width: 29 });
      graph.nodes.find(n => n.kind === 'conv2d').config.stride = 2;
    }
    if (preset === 'cnn_avg_flatten') {
      graph.nodes.find(n => n.kind === 'maxpool2d').kind = 'avgpool2d';
      graph.nodes.find(n => n.kind === 'globalpool2d').kind = 'flatten';
    }
    const analysis = analyzeGraph(graph);
    await writeFile(join(output, `${preset}.py`), generatePython(graph).code);
    manifest[preset] = { parameters: analysis.parameters, inputShape: analysis.shapes[graph.nodes[0].id].dims.map(d => d === 'B' ? 2 : d), inputType: analysis.shapes[graph.nodes[0].id].dtype, outputShape: analysis.shapes[graph.nodes.at(-1).id].dims.map(d => d === 'B' ? 2 : d) };
  }
  await writeFile(join(output, 'manifest.json'), JSON.stringify(manifest, null, 2));
  console.log(`Exported starter modules to ${output}`);
} finally { await server.close(); }
