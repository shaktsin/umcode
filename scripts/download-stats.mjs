import { writeFile } from 'node:fs/promises';
import { pathToFileURL } from 'node:url';

export async function collectStats(loadPage, pageSize = 100) {
  let total = 0;
  for (let page = 1; ; page++) {
    const releases = await loadPage(page, pageSize);
    if (!Array.isArray(releases)) throw new Error('Invalid releases response');
    for (const release of releases) {
      if (release.draft) continue;
      if (!Array.isArray(release.assets)) throw new Error('Missing release assets');
      for (const asset of release.assets) {
        if (asset.name !== 'UMCode.dmg') continue;
        if (!Number.isSafeInteger(asset.download_count) || asset.download_count < 0) {
          throw new Error('Invalid download count');
        }
        total += asset.download_count;
      }
    }
    if (releases.length < pageSize) break;
  }
  return { total, updatedAt: new Date().toISOString() };
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const stats = await collectStats(async (page, pageSize) => {
    const headers = { Accept: 'application/vnd.github+json', 'X-GitHub-Api-Version': '2022-11-28' };
    if (process.env.GITHUB_TOKEN) headers.Authorization = `Bearer ${process.env.GITHUB_TOKEN}`;
    const response = await fetch(`https://api.github.com/repos/shaktsin/umcode/releases?per_page=${pageSize}&page=${page}`, {
      headers, signal: AbortSignal.timeout(30000),
    });
    if (!response.ok) throw new Error(`GitHub releases request failed: ${response.status}`);
    return response.json();
  });
  // Write only after every page succeeds; failed refreshes must not publish a partial count.
  await writeFile(process.argv[2] || 'site/download-stats.json', JSON.stringify(stats) + '\n');
}
