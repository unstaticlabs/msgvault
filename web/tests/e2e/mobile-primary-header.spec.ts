import { expect, test } from '@playwright/test';
import { installMixedArchive } from './fixtures/mixed-archive';

for (const width of [320, 390]) {
  test(`Primary navigation remains reachable by mouse at ${width}px`, async ({ page }) => {
    await installMixedArchive(page);
    await page.setViewportSize({ width, height: 844 });
    await page.goto('/');
    const primary = page.getByRole('button', { name: 'Open navigation' });
    const density = page.getByRole('button', { name: 'Display', exact: true });
    await expect(primary).toBeVisible();
    await expect(density).toBeVisible();
    await expect.poll(async () => {
      const first = await primary.boundingBox();
      const second = await density.boundingBox();
      return first !== null && second !== null && first.x >= 0 && first.x + first.width <= width &&
        second.x >= 0 && second.x + second.width <= width &&
        (first.x + first.width <= second.x || second.x + second.width <= first.x ||
          first.y + first.height <= second.y || second.y + second.height <= first.y);
    }).toBe(true);
    expect(await primary.evaluate((element) => {
      const rect = element.getBoundingClientRect();
      const hit = document.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2);
      return hit !== null && (element === hit || element.contains(hit));
    })).toBe(true);
    await primary.click();
    const navigation = page.getByRole('dialog', { name: 'Navigation', exact: true });
    await expect(navigation.getByRole('button', { name: 'Everything', exact: true })).toBeVisible();
    await expect(page.getByRole('menu', { name: 'Display', exact: true })).not.toBeVisible();
    await navigation.getByRole('button', { name: 'Everything', exact: true }).click();
    await expect(navigation).not.toBeVisible();
    await expect(page.getByRole('main', { name: 'Everything', exact: true })).toBeVisible();
    await density.click();
    await expect(page.getByRole('menuitemradio', { name: 'Auto', exact: true })).toBeVisible();
    await page.getByRole('menuitemradio', { name: 'Comfortable', exact: true }).click();
    await expect(primary).toBeVisible();
  });
}

test('Desktop header retains one row and direct navigation', async ({ page }) => {
  await installMixedArchive(page);
  await page.setViewportSize({ width: 1280, height: 844 });
  await page.goto('/');
  const banner = page.getByRole('banner');
  const navigation = page.getByRole('navigation', { name: 'Primary' });
  const density = page.getByRole('button', { name: 'Display', exact: true });
  await expect(navigation.getByRole('button', { name: 'Everything', exact: true })).toBeVisible();
  const headerBox = await banner.boundingBox();
  const densityBox = await density.boundingBox();
  expect(headerBox).not.toBeNull();
  expect(densityBox).not.toBeNull();
  expect(headerBox!.height).toBeLessThan(64);
  expect(densityBox!.y + densityBox!.height).toBeLessThanOrEqual(headerBox!.y + headerBox!.height);
});

for (const width of [320, 390]) {
  test(`Related email filters and full account identities stay reachable at ${width}px`, async ({ page }) => {
    const fixture = await installMixedArchive(page);
    const email = fixture.logicalRows.find((row) => row.kind === 'email')!;
    const account = 'qa-field-team-with-a-deliberately-long-account-name@example.test';
    const identity = 'long.synthetic.recipient.identity@disposable-mailbox.example.test';
    await page.route('**/api/v1/explore', (route) => route.fulfill({ json: {
      ...fixture.firstPage,
      rows: [{ ...email, source_identifier: account, matched_recipient_identities: [identity],
        email_work: { state: 'actionable', source_ids: [email.source_id], uid: 'synthetic-work',
          evidence_count: 1, outstanding_count: 1, reasons: [] } }],
      total_count: 1, next_cursor: undefined
    } }));
    await page.setViewportSize({ width, height: 844 });
    const filters = [
      { dimension: 'inbox_state', values: ['inbox'] },
      { dimension: 'read_state', values: ['unread'] },
      { dimension: 'row_unit', values: ['email_work_item'] }
    ];
    await page.goto('/?explore=' + encodeURIComponent(JSON.stringify({ workspace: 'everything', filters })));
    const chips = page.locator('.context-chips .chip');
    await expect(chips).toHaveCount(3);
    for (const chip of await chips.all()) {
      const bounds = await chip.boundingBox();
      expect(bounds).not.toBeNull();
      expect(bounds!.x).toBeGreaterThanOrEqual(0);
      expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(width);
      const buttonBounds = await chip.getByRole('button').boundingBox();
      expect(buttonBounds!.x + buttonBounds!.width).toBeLessThanOrEqual(width);
    }
    const row = page.locator('[data-row-key]').first();
    await expect(row.locator('.work-account')).toHaveText(account);
    await expect(row.getByTestId('identity-badge')).toHaveText(`Via: ${identity}`);
    for (const label of [row.locator('.work-account'), row.getByTestId('identity-badge')]) {
      const geometry = await label.evaluate((element) => {
        const rect = element.getBoundingClientRect();
        const rowRect = element.closest('[data-row-key]')!.getBoundingClientRect();
        return { left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom, rowTop: rowRect.top, rowBottom: rowRect.bottom,
          scrollWidth: element.scrollWidth, clientWidth: element.clientWidth };
      });
      expect(geometry.left).toBeGreaterThanOrEqual(0);
      expect(geometry.right).toBeLessThanOrEqual(width);
      expect(geometry.top).toBeGreaterThanOrEqual(geometry.rowTop);
      expect(geometry.bottom).toBeLessThanOrEqual(geometry.rowBottom);
      expect(geometry.scrollWidth).toBeLessThanOrEqual(geometry.clientWidth);
    }
    await chips.filter({ hasText: 'Related email' }).getByRole('button').click();
    await expect(chips).toHaveCount(2);
    await expect(page.getByRole('checkbox', { name: 'Group related email' })).not.toBeChecked();
  });
}
