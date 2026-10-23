import test from 'node:test';
import assert from 'node:assert/strict';
import { collectStats } from './download-stats.mjs';

test('counts installers across pages, excluding checksums, demos and drafts', async () => {
  const pages = [
    [{ assets: [{name:'UMCode.dmg',download_count:13},{name:'UMCode.dmg.sha256',download_count:9}] }],
    [{ assets: [{name:'UMCode.dmg',download_count:1},{name:'umaBot.mp4',download_count:10}] }, {draft:true,assets:[{name:'UMCode.dmg',download_count:50}]}],
  ];
  const result = await collectStats(async () => pages.shift() ?? [], 1);
  assert.equal(result.total, 14);
  assert.ok(!Number.isNaN(Date.parse(result.updatedAt)));
});

test('API failures and malformed counts cannot publish a false total', async () => {
  await assert.rejects(collectStats(async () => {throw new Error('API unavailable');}));
  await assert.rejects(collectStats(async () => [{assets:[{name:'UMCode.dmg',download_count:-1}]}]));
});
