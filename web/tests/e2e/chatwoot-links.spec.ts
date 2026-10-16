import { test, expect, loginToMeetingArchive } from './fixtures/meeting-daemon';

for (const viewport of [{ width: 1280, height: 720 }, { width: 390, height: 844 }]) {
  test(`call and meeting links round trip through the real archive at ${viewport.width}px`, async ({ page, daemon }) => {
    await page.setViewportSize(viewport);
    await loginToMeetingArchive(page, daemon);
    const [callID, meetingID] = daemon.chatwoot;
    await page.goto(`${daemon.origin}/messages/${callID}`);
    const call = page.getByRole('article', { name: `Message ${callID}`, exact: true });
    await expect(call).toContainText('Voice call with Example Customer');
    const meetingLink = call.getByRole('link', { name: 'Open meeting', exact: true });
    await expect(meetingLink).toBeVisible();
    const bounds = await meetingLink.boundingBox();
    expect(bounds).not.toBeNull();
    expect(bounds!.x).toBeGreaterThanOrEqual(0);
    expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(viewport.width);
    await meetingLink.click();
    const meeting = page.getByRole('article', { name: `Message ${meetingID}`, exact: true });
    await expect(meeting).toContainText('Please send the meeting recap');
    await page.reload();
    await expect(meeting).toContainText('Please send the meeting recap');
    await page.goBack();
    await expect(call).toBeVisible();
    await meetingLink.click();
    await expect(meeting).toBeVisible();
    const download = page.waitForEvent('download');
    await meeting.getByRole('list', { name: 'Attachments' }).getByRole('link').click();
    expect(await (await download).failure()).toBeNull();
    await meeting.getByRole('link', { name: 'Open conversation', exact: true }).click();
    await expect(page).toHaveURL(`${daemon.origin}/messages/${callID}`);
    await expect(call).toHaveAttribute('aria-current', 'true');
    // Narrow readers intentionally hide collapsed previews. The native expand
    // control must still make the preceding conversation message readable.
    await page.getByRole('button', { name: /Expand message 1 from/ }).click();
    await expect(page.getByRole('region', { name: 'Message body' }).filter({ hasText: 'Could we discuss the next delivery?' })).toBeVisible();
  });
}
