import { test, expect, Page } from '@playwright/test';
import { ADMIN, signIn } from './support';

const run = () => Date.now().toString(36).toUpperCase() + Math.floor(Math.random() * 1000);

async function openAccount(
  page: Page,
  code: string,
  capacity: number,
  shown: string,
  limit = capacity,
) {
  await signIn(page, ADMIN);
  await page.getByLabel('Drop code').fill(code);
  await page.getByLabel('Units on sale').fill(String(capacity));
  await page.getByLabel('Limit per collector').fill(String(limit));
  await page.getByRole('button', { name: 'Add drop' }).click();
  await expect(page.getByRole('heading', { name: `Buy units for ${code}` })).toBeVisible();
  await expect(page.getByTestId('selected-remaining')).toHaveText(shown);
}

async function submit(page: Page, reference: string, quantity: number) {
  await page.getByLabel('Collector reference').fill(reference);
  await page.getByLabel('Units', { exact: true }).fill(String(quantity));
  await page.getByRole('button', { name: 'Buy' }).click();
}

const row = (page: Page, reference: string) => page.locator(`tr[data-reference="${reference}"]`);

test('the API refuses bad input and the page says why', async ({ page }) => {
  const id = run();
  await openAccount(page, `UI-ERR-${id}`, 5, '5');

  await submit(page, `COL-${id}`, 7);
  await expect(page.getByRole('alert')).toContainText('at most 6 units');

  await submit(page, ' ', 1);
  await expect(page.getByRole('alert')).toContainText(/reference is required/i);

  await submit(page, `COL-${id}`, 1);
  await expect(row(page, `COL-${id}`).getByTestId('status')).toHaveText('Sold', {
    timeout: 20_000,
  });
  await submit(page, `COL-${id}`, 1);
  await expect(page.getByRole('alert')).toContainText('already');
  await expect(page.locator('tbody tr[data-reference]')).toHaveCount(1);
});

test('a poison reference is rejected, gets no document, and the amount comes back', async ({
  page,
}) => {
  const id = run();
  await openAccount(page, `UI-POI-${id}`, 5, '5');

  await submit(page, `COL-POISON-${id}`, 2);
  const poisoned = row(page, `COL-POISON-${id}`);
  await expect(poisoned.getByTestId('status')).toHaveText('Rejected', { timeout: 20_000 });
  await expect(poisoned).toContainText('poison');
  await expect(poisoned.getByTestId('certificate')).toHaveText('—');
  await expect(poisoned.getByRole('button', { name: 'Refund' })).toHaveCount(0);
  await expect(page.getByTestId('selected-remaining')).toHaveText('5');
});

test('the last unit goes to one request and the same collector asking again is held to the limit', async ({
  page,
}) => {
  const id = run();
  await openAccount(page, `UI-LAST-${id}`, 1, '1');

  await submit(page, `COL-A-${id}`, 1);
  await submit(page, `COL-B-${id}`, 1);

  const statuses = page.locator('tbody tr[data-reference] [data-testid="status"]');
  await expect(statuses).toHaveCount(2);
  await expect
    .poll(async () => (await statuses.allTextContents()).map((s) => s.trim()).sort(), {
      timeout: 20_000,
    })
    .toEqual(['Sold', 'Rejected'].sort());
  await expect(page.locator('tbody')).toContainText('per collector');
  await expect(page.getByTestId('selected-remaining')).toHaveText('0');
});

test('the event log shows the Kafka steps and the reversal keeps the document', async ({
  page,
}) => {
  const id = run();
  await openAccount(page, `UI-LOG-${id}`, 4, '4');

  await submit(page, `COL-${id}`, 2);
  const mine = row(page, `COL-${id}`);
  await expect(mine.getByTestId('status')).toHaveText('Sold', { timeout: 20_000 });

  await mine.getByRole('button', { name: 'Event log' }).click();
  const log = page.getByRole('region', { name: 'Kafka event log' });
  await expect(log).toContainText('units.reserve', { timeout: 20_000 });
  await expect(log).toContainText('units.held');
  await expect(log).toContainText('certificate.issued');

  await mine.getByRole('button', { name: 'Refund' }).click();
  await expect(mine.getByTestId('status')).toHaveText('Refunded');
  await expect(mine.getByRole('button', { name: 'Refund' })).toHaveCount(0);
  await expect(mine.getByTestId('certificate')).toHaveText(/MC-[0-9A-F]{12}/);
  await expect(page.getByTestId('selected-remaining')).toHaveText('4');
  await expect(log).toContainText('units.released', { timeout: 20_000 });
});

test('one collector cannot go past the limit per collector, and a refund gives the allowance back', async ({
  page,
}) => {
  const id = run();
  await openAccount(page, `UI-CAP-${id}`, 10, '10', 2);
  await expect(page.getByTestId(`remaining-UI-CAP-${id}`)).toContainText('max 2 each');

  await submit(page, `COL-ONE-${id}`, 2);
  await expect(row(page, `COL-ONE-${id}`).getByTestId('status')).toHaveText('Sold', {
    timeout: 20_000,
  });

  await submit(page, `COL-TWO-${id}`, 1);
  const capped = row(page, `COL-TWO-${id}`);
  await expect(capped.getByTestId('status')).toHaveText('Rejected', { timeout: 20_000 });
  await expect(capped).toContainText('at most 2 units per collector');
  await expect(page.getByTestId('selected-remaining')).toHaveText('8');

  await row(page, `COL-ONE-${id}`).getByRole('button', { name: 'Refund' }).click();
  await expect(row(page, `COL-ONE-${id}`).getByTestId('status')).toHaveText('Refunded', {
    timeout: 20_000,
  });
  await expect(page.getByTestId('selected-remaining')).toHaveText('10');

  await submit(page, `COL-THREE-${id}`, 2);
  await expect(row(page, `COL-THREE-${id}`).getByTestId('status')).toHaveText('Sold', {
    timeout: 20_000,
  });
});
