import { expect, test } from '@playwright/test';

import en from '../../src/__locales/en.json' with { type: 'json' };
import { login } from '../helpers/login';

test.describe('Login', () => {
    test('should successfully log in with valid credentials', async ({ page }) => {
        await login(page);
    });

    test('opens the default dashboard from the old frontend URL', async ({ page }) => {
        await page.goto('/client_v2/');
        await expect(page).toHaveURL(/\/login\.html$/);
        await expect(page.locator('#username')).toBeVisible();

        await login(page);
        await expect(page).toHaveURL(/\/#\/?dashboard$/);
    });

    test('opens GFW List from the new dashboard', async ({ page }) => {
        await login(page);
        await page.getByRole('button', { name: en.filters, exact: true }).click();

        const status = page.waitForResponse(
            (response) => new URL(response.url()).pathname === '/control/gfwlist/status',
        );
        await page.getByRole('link', { name: en.gfwlist_title, exact: true }).click();

        await expect(page).toHaveURL(/\/legacy\.html#gfwlist$/);
        await expect(page.locator('#gfwlist-url')).toBeVisible();
        expect((await status).ok()).toBe(true);
    });
});
