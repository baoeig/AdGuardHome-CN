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

    test('opens and manages GFW List in the new dashboard', async ({ page }) => {
        await login(page);
        await page.getByRole('button', { name: en.filters, exact: true }).click();

        const status = page.waitForResponse(
            (response) => new URL(response.url()).pathname === '/control/gfwlist/status',
        );
        await page.getByRole('link', { name: en.gfwlist_title, exact: true }).click();

        await expect(page).toHaveURL(/\/#\/gfwlist$/);
        await expect(page.locator('#gfwlist-url')).toBeVisible();
        expect((await status).ok()).toBe(true);

        await page.locator('#gfwlist-url').fill('https://example.org/test-list.txt');
        await page.locator('#gfwlist-interval').fill('6');
        const saved = page.waitForResponse(
            (response) => new URL(response.url()).pathname === '/control/gfwlist/config',
        );
        await page.locator('#gfwlist-save').click();
        expect((await saved).ok()).toBe(true);

        const domain = 'gfw-browser.example';
        await page.locator('#gfwlist-new-domain').fill(`*.${domain}\nlocalhost`);
        await page.locator('#gfwlist-add-domain').click();
        await expect(page.getByText(domain, { exact: true })).toBeVisible();

        await page.locator('#gfwlist-check-domain').fill(domain);
        await page.getByRole('button', { name: en.gfwlist_check_domain, exact: true }).click();
        await expect(
            page.getByText(en.gfwlist_check_not_matched.replace('%domain%', domain)),
        ).toBeVisible();

        await page
            .getByRole('button', {
                name: en.gfwlist_aria_remove_domain.replace('%domain%', domain),
            })
            .click();
        await expect(page.getByText(domain, { exact: true })).toHaveCount(0);

        await page.goto('/legacy.html#gfwlist');
        await expect(page).toHaveURL(/\/#\/gfwlist$/);
        await expect(page.locator('#gfwlist-url')).toHaveValue('https://example.org/test-list.txt');
        await expect(page.locator('#gfwlist-interval')).toHaveValue('6');
    });
});
