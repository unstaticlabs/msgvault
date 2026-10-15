import { expect, test } from '@playwright/test';
import { installMixedArchive } from './fixtures/mixed-archive';

for (const width of [320, 390]) {
  test(`Primary navigation remains reachable by mouse at ${width}px`, async ({ page }) => {
    await installMixedArchive(page);
    await page.setViewportSize({ width, height: 844 });
    await page.goto('/');
    const primary = page.getByRole('navigation', { name: 'Primary' })
      .getByRole('combobox', { name: /^Primary:/ });
    const density = page.getByRole('combobox', { name: /^Temporary density:/ });
    await expect(primary).toBeVisible();
    await expect(density).toBeVisible();
    await expect.poll(async () => {
      const first = await primary.boundingBox();
      const second = await density.boundingBox();
      return first !== null && second !== null && first.x >= 0 && first.x + first.width <= width &&
        second.x >= 0 && second.x + second.width <= width &&
        (first.y + first.height <= second.y || second.y + second.height <= first.y);
    }).toBe(true);
    expect(await primary.evaluate((element) => {
      const rect = element.getBoundingClientRect();
      const hit = document.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2);
      return hit !== null && (element === hit || element.contains(hit));
    })).toBe(true);
    await primary.click();
    await expect(page.getByRole('option', { name: 'Everything', exact: true })).toBeVisible();
    await expect(page.getByRole('option', { name: 'Density: Auto', exact: true })).not.toBeVisible();
    await page.getByRole('option', { name: 'Everything', exact: true }).click();
    await expect(primary).toHaveAccessibleName('Primary: Everything');
  });
}

test('Desktop header retains one row and direct navigation', async ({ page }) => {
  await installMixedArchive(page);
  await page.setViewportSize({ width: 1280, height: 844 });
  await page.goto('/');
  const banner = page.getByRole('banner');
  const navigation = page.getByRole('navigation', { name: 'Primary' });
  const density = page.getByRole('combobox', { name: /^Temporary density:/ });
  await expect(navigation.getByRole('button', { name: 'Everything', exact: true })).toBeVisible();
  const headerBox = await banner.boundingBox();
  const densityBox = await density.boundingBox();
  expect(headerBox).not.toBeNull();
  expect(densityBox).not.toBeNull();
  expect(headerBox!.height).toBeLessThan(64);
  expect(densityBox!.y + densityBox!.height).toBeLessThanOrEqual(headerBox!.y + headerBox!.height);
});
