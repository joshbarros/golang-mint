import { test, expect } from '@playwright/test';
import { ADMIN, adminToken, signIn } from './support';

test('add a drop, buy units, see the certificate, refund it', async ({ page }) => {
  const run = Date.now().toString(36).toUpperCase();
  const code = `E2E-${run}`;
  const reference = `COL-${run}`;

  await signIn(page, ADMIN);
  await page.getByLabel('Drop code').fill(code);
  await page.getByLabel('Units on sale').fill('8');
  await page.getByLabel('Limit per collector').fill('8');
  await page.getByRole('button', { name: 'Add drop' }).click();

  const remaining = page.getByTestId('selected-remaining');
  await expect(page.getByRole('heading', { name: `Buy units for ${code}` })).toBeVisible();
  await expect(remaining).toHaveText('8');

  await page.getByLabel('Collector reference').fill(reference);
  await page.getByLabel('Units', { exact: true }).fill('3');
  await page.getByRole('button', { name: 'Buy' }).click();

  const row = page.locator(`tr[data-reference="${reference}"]`);
  await expect(row.getByTestId('status')).toHaveText('Sold', { timeout: 20_000 });
  await expect(row.getByTestId('certificate')).toHaveText(/MC-[0-9A-F]{12}/);
  await expect(remaining).toHaveText('5');

  // Nothing is kept in the browser: a fresh load reads the same state from the API.
  // Nothing sensitive is stored either: the refresh cookie is HttpOnly, so a reload signs back in through it.
  expect(await page.evaluate(() => JSON.stringify({ ...localStorage, ...sessionStorage }))).toBe(
    '{}',
  );
  await page.reload();
  await expect(page.getByTestId('whoami')).toContainText(ADMIN.email);
  await page.getByRole('button', { name: new RegExp(`^${code}`) }).click();
  const reloaded = page.locator(`tr[data-reference="${reference}"]`);
  await expect(reloaded.getByTestId('status')).toHaveText('Sold');
  await expect(page.getByTestId('selected-remaining')).toHaveText('5');

  await reloaded.getByRole('button', { name: 'Refund' }).click();
  await expect(reloaded.getByTestId('status')).toHaveText('Refunded');
  await expect(page.getByTestId('selected-remaining')).toHaveText('8');
  // The issued certificate is not rolled back.
  await expect(reloaded.getByTestId('certificate')).toHaveText(/MC-[0-9A-F]{12}/);
  const headers = { Authorization: `Bearer ${await adminToken(page.request)}` };
  const orders = await page.request.get(`/api/orders?dropCode=${code}`, { headers });
  const [order] = await orders.json();
  const certificate = await page.request.get(`/api/certificates/${order.id}`, { headers });
  expect((await certificate.json()).status).toBe('Issued');
});
