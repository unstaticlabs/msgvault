import { expect, test } from '@playwright/test';
import { mkdir } from 'node:fs/promises';
import path from 'node:path';
import { captureScreenshot } from './docs-fixture-screenshot';
import { selectKitOption } from './kit-ui';

// Manual native-daemon lane: a disposable three-account fixture has one
// unread Inbox message and one read Inbox message per account. This makes no
// provider request and never redirects API calls to mocked responses.
const output = process.env.MSGVAULT_INBOX_QA_OUTPUT;
test.skip(!output, 'requires the disposable native Inbox daemon fixture');
for (const viewport of [{ name: 'desktop', width: 1280, height: 800 }, { name: 'mobile', width: 390, height: 844 }]) {
  test(`native Saved View and mailbox filters — ${viewport.name}`, async ({ page }) => {
    await mkdir(output!, { recursive: true });
    await page.setViewportSize(viewport);
    await page.goto('/?explore=' + encodeURIComponent(JSON.stringify({ workspace: 'saved_views' })));
    await expect(page.getByRole('main', { name: 'Saved Views' })).toBeVisible();
    await page.getByRole('button', { name: 'Open [QA] Inbox Unread', exact: true }).click();
    const grid = page.getByRole('grid', { name: 'Everything results' });
    await expect(grid).toBeVisible();
    await expect(grid.locator('[data-row-key]')).toHaveCount(3);
    await expect(grid.locator('[data-row-key]').first()).toContainText('Unread · In inbox');
    for (const label of ['In inbox', 'Unread']) {
      const chip = page.locator('.context-crumbs .crumb').filter({ hasText: new RegExp(`^${label}$`) });
      await expect(chip).toBeVisible();
      const box = await chip.boundingBox();
      expect(box).not.toBeNull();
      expect(box!.x).toBeGreaterThanOrEqual(0);
      expect(box!.x + box!.width).toBeLessThanOrEqual(viewport.width);
      expect(box!.y + box!.height).toBeLessThanOrEqual(viewport.height);
    }
    const caption = grid.locator('[data-row-key]').first().locator('.mailbox-state');
    const captionBox = await caption.boundingBox();
    expect(captionBox).not.toBeNull();
    expect(captionBox!.x + captionBox!.width).toBeLessThanOrEqual(viewport.width);
    await captureScreenshot(page, path.join(output!, `inbox-unread-${viewport.name}.png`));
    await page.getByRole('button', { name: 'Filters', exact: true }).click();
    await expect(page.getByRole('combobox', { name: 'Inbox: In inbox', exact: true })).toBeVisible();
    await expect(page.getByRole('combobox', { name: 'Read status: Unread', exact: true })).toBeVisible();
    await captureScreenshot(page, path.join(output!, `inbox-filters-${viewport.name}.png`));
    await selectKitOption(page, 'Read status', 'All read states');
    await expect(grid.locator('[data-row-key]')).toHaveCount(6);
    await expect(page.getByRole('combobox', { name: 'Inbox: In inbox', exact: true })).toBeVisible();
    await selectKitOption(page, 'Read status', 'Read');
    await expect(grid.locator('[data-row-key]')).toHaveCount(3);
    await expect(grid.locator('[data-row-key]').first()).toContainText('Read · In inbox');
  });
}

test('repairs a persisted legacy people filter only after explicit Save', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  const filters = [{ dimension: 'source', values: ['1', '2', '3'] }, { dimension: 'inbox_state', values: ['inbox'] }, { dimension: 'read_state', values: ['unread'] }];
  await page.goto('/?explore=' + encodeURIComponent(JSON.stringify({ workspace: 'saved_views', filters })));
  const name = '[QA] Older people view';
  const before = (await (await page.request.get('/api/v1/saved-views')).json()).saved_views.find((view: { name: string }) => view.name === name);
  expect(before).toBeDefined();
  await expect(page.getByRole('button', { name: `Open ${name}`, exact: true })).toBeDisabled();
  await expect(page.getByRole('alert')).toContainText('Select the people again in Explore and save a new view');
  const notice = page.getByRole('alert');
  expect((await notice.boundingBox())!.width).toBeGreaterThan(500);
  await captureScreenshot(page, path.join(output!, 'saved-view-legacy-desktop.png'));
  await page.setViewportSize({ width: 390, height: 844 });
  const mobileNotice = await notice.boundingBox();
  expect(mobileNotice!.x).toBeGreaterThanOrEqual(0);
  expect(mobileNotice!.x + mobileNotice!.width).toBeLessThanOrEqual(390);
  await captureScreenshot(page, path.join(output!, 'saved-view-legacy-mobile.png'));
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.getByRole('button', { name: `Repair filters for ${name}`, exact: true }).click();
  await expect(page.getByRole('list', { name: 'Replacement filters' })).toContainText('Unread');
  const unchanged = await (await page.request.get(`/api/v1/saved-views/${before.id}`)).json();
  expect(unchanged.revision).toBe(before.revision);
  expect(unchanged.canonical_state).toEqual(before.canonical_state);
  await captureScreenshot(page, path.join(output!, 'saved-view-repair-desktop.png'));
  await page.setViewportSize({ width: 390, height: 844 });
  const saveButton = page.getByRole('button', { name: 'Save repaired filters', exact: true });
  const mobileSave = await saveButton.boundingBox();
  expect(mobileSave!.x + mobileSave!.width).toBeLessThanOrEqual(390);
  await captureScreenshot(page, path.join(output!, 'saved-view-repair-mobile.png'));
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.getByRole('button', { name: 'Save repaired filters', exact: true }).click();
  await expect(page.getByRole('button', { name: `Open ${name}`, exact: true })).toBeEnabled();
  const after = await (await page.request.get(`/api/v1/saved-views/${before.id}`)).json();
  expect(after.revision).toBe(before.revision + 1);
  expect(after.canonical_state.filters).toEqual(filters.map((filter) => ({ field: filter.dimension, operator: 'in', values: filter.values })));
  await page.getByRole('button', { name: `Open ${name}`, exact: true }).click();
  await expect(page.getByRole('grid', { name: 'Everything results' }).locator('[data-row-key]')).toHaveCount(3);
});


test('keeps native empty identity direction through Open and URL restoration', async ({ page }) => {
  await page.goto('/?explore=' + encodeURIComponent(JSON.stringify({ workspace: 'saved_views' })));
  const open = page.getByRole('button', { name: 'Open [QA] Identity default direction', exact: true });
  await expect(open).toBeEnabled();
  const response = page.waitForResponse((response) => response.request().method() === 'POST' && new URL(response.url()).pathname === '/api/v1/explore');
  await open.click();
  const actual = await response;
  const identity = actual.request().postDataJSON().filters.find((filter: { dimension: string }) => filter.dimension === 'identity');
  expect(identity.values).toEqual(['1', 'qa-account-1@example.com', '']);
  expect(actual.status()).toBe(200);
  const restored = JSON.parse(new URL(page.url()).searchParams.get('explore')!);
  expect(restored.filters.find((filter: { dimension: string }) => filter.dimension === 'identity').values).toEqual(identity.values);
});
