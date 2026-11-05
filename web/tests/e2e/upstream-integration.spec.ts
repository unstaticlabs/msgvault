import { expect, test } from '@playwright/test';
import { installMixedArchive } from './fixtures/mixed-archive';

for (const width of [390, 1280]) {
  test(`Messages retain mailbox filters in the native toolbar at ${width}px`, async ({ page }, info) => {
    await installMixedArchive(page);
    await page.setViewportSize({ width, height: 900 });
    await page.goto('/?explore=' + encodeURIComponent(JSON.stringify({
      workspace: 'everything', filters: [
        { dimension: 'inbox_state', values: ['inbox'] },
        { dimension: 'read_state', values: ['unread'] },
      ],
    })));
    await expect(page.getByRole('grid', { name: 'Messages results' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Remove Inbox filter' })).toBeVisible();
    await page.getByRole('button', { name: 'Filter', exact: true }).click();
    await expect(page.getByRole('combobox', { name: 'Inbox: In inbox' })).toBeVisible();
    await expect(page.getByRole('combobox', { name: 'Read status: Unread' })).toBeVisible();
    await page.screenshot({ path: info.outputPath(`mailbox-toolbar-${width}.png`), fullPage: true });
    await page.keyboard.press('Escape');
    await page.getByRole('button', { name: 'Remove Read status filter' }).click();
    await expect(page.getByRole('button', { name: 'Remove Inbox filter' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Remove Read status filter' })).toHaveCount(0);
  });

  test(`Saved view recovery preserves the original until explicit save at ${width}px`, async ({ page }, info) => {
    await installMixedArchive(page);
    await page.setViewportSize({ width, height: 900 });
    let view = {
      id: 7, name: 'Synthetic follow-up', description: 'Disposable upgrade fixture',
      schema_version: 1, revision: 3,
      created_at: '2026-11-05T09:00:00Z', updated_at: '2026-11-05T09:00:00Z',
      incompatibility_reason: 'Select the person again to repair this old filter.',
      canonical_state: { query: 'alpha | beta', search_mode: 'full_text', filters: [
        { field: 'participant', operator: 'in', values: ['obsolete@example.test'] },
      ] },
    };
    const writes: { body: Record<string, unknown>; revision: string | undefined }[] = [];
    await page.route('**/api/v1/saved-views', route => route.fulfill({ json: { saved_views: [view] } }));
    await page.route('**/api/v1/saved-views/7', async route => {
      const body = route.request().postDataJSON();
      writes.push({ body, revision: route.request().headers()['if-match'] });
      view = { ...view, ...body, revision: 4, incompatibility_reason: '' };
      await route.fulfill({ json: view });
    });
    await page.goto('/?explore=' + encodeURIComponent(JSON.stringify({
      workspace: 'saved_views', filters: [{ dimension: 'source', values: ['1'] }],
    })));
    await page.getByRole('button', { name: 'Repair filters' }).click();
    await expect(page.getByRole('list', { name: 'Replacement filters' })).toContainText('Source: 1');
    expect(writes).toEqual([]);
    await page.screenshot({ path: info.outputPath(`saved-view-repair-${width}.png`), fullPage: true });
    await page.getByRole('button', { name: 'Save repaired filters' }).click();
    await expect(page.getByRole('button', { name: 'Open Synthetic follow-up' })).toBeEnabled();
    expect(writes).toHaveLength(1);
    expect(writes[0]?.revision).toBe('"saved-view-7-r3"');
    expect(writes[0]?.body).not.toHaveProperty('schema_version');
    expect(writes[0]?.body.canonical_state).toMatchObject({
      query: 'alpha | beta', filters: [{ field: 'source', operator: 'in', values: ['1'] }],
    });
  });

  test(`The native person page keeps details and files reachable at ${width}px`, async ({ page }, info) => {
    await installMixedArchive(page);
    await page.setViewportSize({ width, height: 900 });
    await page.goto('/?explore=' + encodeURIComponent(JSON.stringify({
      workspace: 'relationships', directoryPersonID: 42,
    })));
    const person = page.getByRole('group', { name: 'Person page' });
    await expect(person.getByRole('heading', { name: 'Archive Person', exact: true })).toBeVisible();
    const tabs = person.getByRole('radiogroup', { name: 'Person tabs' });
    await tabs.getByRole('radio', { name: 'Files', exact: true }).click();
    await expect(person.getByRole('grid', { name: 'Files results' })).toContainText('durable-person.pdf');
    await page.screenshot({ path: info.outputPath(`person-files-${width}.png`), fullPage: true });
    await tabs.getByRole('radio', { name: 'Details', exact: true }).click();
    await expect(tabs.getByRole('radio', { name: 'Details', exact: true })).toHaveAttribute('aria-checked', 'true');
  });
}
