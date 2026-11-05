import { expect, test, type Locator } from '@playwright/test';
import { setTemporaryDensity, setKitTheme } from '../kit-ui';
import { installMixedArchive } from './fixtures/mixed-archive';

async function fitsHeader(control: Locator, header: Locator, viewportWidth: number) {
  const bounds = await control.boundingBox();
  const pane = await header.boundingBox();
  return bounds !== null && pane !== null && bounds.width > 0 && bounds.height > 0 &&
    bounds.x >= pane.x - 1 && bounds.x + bounds.width <= pane.x + pane.width + 1 &&
    bounds.x + bounds.width <= viewportWidth;
}

for (const width of [320, 390, 1280]) {
  for (const theme of ['light', 'dark'] as const) {
    for (const density of ['compact', 'comfortable'] as const) {
      test(`Person title and actions fit ${width}px ${theme}/${density}`, async ({ page }) => {
        await installMixedArchive(page);
        await page.setViewportSize({ width, height: 844 });
        await page.goto('/?explore=' + encodeURIComponent(JSON.stringify({
          workspace: 'relationships', directoryPersonID: 42,
        })));
        await setKitTheme(page, theme);
        await setTemporaryDensity(page, density === 'compact' ? 'Compact' : 'Comfortable');
        const person = page.getByRole('group', { name: 'Person page' });
        const header = person.locator('header[aria-label="Person detail"]');
        const heading = header.getByRole('heading', { name: 'Archive Person', exact: true });
        await expect.poll(() => fitsHeader(heading, header, width)).toBe(true);
        expect(await heading.evaluate((element) => element.scrollWidth <= element.clientWidth &&
          element.scrollHeight <= element.clientHeight)).toBe(true);
        const action = header.getByRole('button', { name: 'Merge with…', exact: true });
        await expect.poll(() => fitsHeader(action, header, width)).toBe(true);
        expect(await action.evaluate((element) => {
          const box = element.getBoundingClientRect();
          const hit = document.elementFromPoint(box.x + box.width / 2, box.y + box.height / 2);
          return hit !== null && (element === hit || element.contains(hit));
        })).toBe(true);
        const tabs = person.getByRole('radiogroup', { name: 'Person tabs' });
        const files = tabs.getByRole('radio', { name: 'Files', exact: true });
        await files.click();
        await expect(files).toHaveAttribute('aria-checked', 'true');
        await expect(person.getByRole('grid', { name: 'Files results' }).getByText('durable-person.pdf')).toBeVisible();
        await expect.poll(() => fitsHeader(heading, header, width)).toBe(true);
        await tabs.getByRole('radio', { name: 'Messages', exact: true }).click();
        await expect(files).toHaveAttribute('aria-checked', 'false');
        await expect.poll(() => fitsHeader(heading, header, width)).toBe(true);
      });
    }
  }
}
