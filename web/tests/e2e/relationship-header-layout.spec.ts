import { expect, test, type Locator } from '@playwright/test';
import { selectKitOption, setKitTheme } from '../kit-ui';
import { installMixedArchive } from './fixtures/mixed-archive';

// The optional native lane uses only a separately provisioned synthetic MIME
// archive. Ordinary CI uses the same rendered components with archive fixtures.
const nativeURL = process.env.MSGVAULT_RELATIONSHIP_HEADER_QA_URL;
const nativeKey = process.env.MSGVAULT_RELATIONSHIP_HEADER_QA_KEY;

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
      test(`Relationship title and actions fit ${width}px ${theme}/${density}`, async ({ page }) => {
        if (nativeURL) {
          expect(new URL(nativeURL).hostname).toBe('127.0.0.1');
          expect(nativeKey).toBeTruthy();
          await page.goto(nativeURL);
          await page.getByLabel('API key', { exact: true }).fill(nativeKey!);
          await page.getByRole('button', { name: 'Log in', exact: true }).click();
        } else {
          await installMixedArchive(page);
          await page.goto('/');
        }
        await setKitTheme(page, theme);
        await selectKitOption(page, 'Temporary density', `Density: ${density === 'compact' ? 'Compact' : 'Comfortable'}`);
        if (nativeURL) {
          // A one-message import is below the default relationship threshold.
          // Include this synthetic counterpart through the ordinary control.
          const allSenders = page.getByRole('button', { name: 'All senders', exact: true });
          if (await allSenders.getAttribute('aria-pressed') !== 'true') await allSenders.click();
        }
        const person = nativeURL ? 'QA File Sender' : 'Archive Person';
        await page.getByRole('grid', { name: 'Relationship results' }).getByText(person, { exact: true }).click();
        await page.setViewportSize({ width, height: 844 });
        const header = page.locator('header[aria-label="Relationship detail"]');
        const heading = header.getByRole('heading', { name: person, exact: true });
        await expect.poll(async () => {
          const bounds = await heading.boundingBox();
          return bounds !== null && bounds.width >= 80 && await fitsHeader(heading, header, width);
        }).toBe(true);
        expect(await heading.evaluate((element) => element.scrollWidth <= element.clientWidth &&
          element.scrollHeight <= element.clientHeight)).toBe(true);
        for (const name of ['Files 1', 'Open in Directory', 'Same person…']) {
          const action = header.getByRole(name === 'Files 1' ? 'radio' : 'button', { name, exact: true });
          await expect.poll(() => fitsHeader(action, header, width)).toBe(true);
          expect(await action.evaluate((element) => {
            const box = element.getBoundingClientRect();
            const hit = document.elementFromPoint(box.x + box.width / 2, box.y + box.height / 2);
            return hit !== null && (element === hit || element.contains(hit));
          })).toBe(true);
        }
        if (width === 1280) {
          const title = await heading.boundingBox();
          const action = await header.getByRole('button', { name: 'Same person…', exact: true }).boundingBox();
          expect(title).not.toBeNull();
          expect(action).not.toBeNull();
          expect(Math.abs(title!.y + title!.height / 2 - action!.y - action!.height / 2)).toBeLessThan(2);
        }
        const files = header.getByRole('radio', { name: 'Files 1', exact: true });
        const fileGrid = page.getByRole('grid', { name: 'Files results' });
        await files.click();
        await expect(files).toHaveAttribute('aria-checked', 'true');
        await expect(fileGrid).toBeVisible();
        if (nativeURL) await expect(fileGrid).toContainText('synthetic-review.txt');
        await expect.poll(() => fitsHeader(heading, header, width)).toBe(true);
        await header.getByRole('radio', { name: 'Messages', exact: true }).click();
        await expect(files).toHaveAttribute('aria-checked', 'false');
        await expect(fileGrid).toBeHidden();
        await expect.poll(() => fitsHeader(heading, header, width)).toBe(true);
      });
    }
  }
}
