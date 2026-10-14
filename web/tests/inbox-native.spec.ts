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
