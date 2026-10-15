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
