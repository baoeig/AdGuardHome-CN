import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { GfwList } from 'panel/components/GfwList';
import { getGfwListStatus, setGfwListConfig, updateGfwList } from 'panel/api/generated';
import { copy } from 'panel/__tests__/helpers/copy';
import { addNoticeToast, addSuccessToast } from 'panel/stores/toasts';

vi.mock('panel/common/intl', async () =>
    (await import('panel/__tests__/helpers/copy')).createIntlMock(),
);

vi.mock('panel/api/generated', () => ({
    getGfwListStatus: vi.fn(),
    setGfwListConfig: vi.fn(),
    updateGfwList: vi.fn(),
    addGfwListDomains: vi.fn(),
    removeGfwListDomains: vi.fn(),
    checkGfwListDomain: vi.fn(),
}));

vi.mock('panel/stores/toasts', () => ({
    addErrorToast: vi.fn(),
    addNoticeToast: vi.fn(),
    addSuccessToast: vi.fn(),
}));

const initialStatus = {
    enabled: true,
    url: 'https://example.org/list.txt',
    upstream_dns: ['1.1.1.1'],
    update_interval: 21600,
    custom_domains: ['first.example.org'],
    custom_domain_page: 0,
    custom_domains_total: 51,
    domain_count: 51,
};

describe('GfwList', () => {
    beforeEach(() => {
        vi.resetAllMocks();
        vi.mocked(getGfwListStatus).mockResolvedValue(initialStatus);
    });

    it('keeps unsaved settings while switching domain pages', async () => {
        const { container } = render(() => <GfwList />);
        const url = await screen.findByLabelText(copy('gfwlist_url'));
        await waitFor(() => expect(url).toBeEnabled());

        fireEvent.input(url, { target: { value: 'https://example.net/new.txt' } });
        vi.mocked(getGfwListStatus).mockResolvedValue({
            ...initialStatus,
            custom_domains: ['second.example.org'],
            custom_domain_page: 1,
        });
        const next = screen.getByRole('button', { name: copy('aria_next_page') });
        fireEvent.click(next);

        await screen.findByText('second.example.org');
        expect(url).toHaveValue('https://example.net/new.txt');

        const save = container.querySelector('#gfwlist-save');
        await waitFor(() => expect(save).toBeEnabled());
        fireEvent.submit(url.closest('form') ?? url);
        await waitFor(() =>
            expect(setGfwListConfig).toHaveBeenCalledWith(
                {
                    enabled: true,
                    url: 'https://example.net/new.txt',
                    upstream_dns: ['1.1.1.1'],
                    update_interval: 21600,
                },
                expect.anything(),
            ),
        );
    });

    it('notifies when a list update is already running', async () => {
        vi.mocked(updateGfwList).mockRejectedValue(
            new Error('control/gfwlist/update | busy | 409'),
        );
        render(() => <GfwList />);
        const update = await screen.findByRole('button', { name: copy('gfwlist_update_now') });
        await waitFor(() => expect(update).toBeEnabled());
        fireEvent.click(update);

        await waitFor(() =>
            expect(addNoticeToast).toHaveBeenCalledWith(copy('gfwlist_update_in_progress')),
        );
    });

    it('uses the update count when the status refresh fails', async () => {
        vi.mocked(getGfwListStatus)
            .mockResolvedValueOnce(initialStatus)
            .mockRejectedValueOnce(new Error('status unavailable'));
        vi.mocked(updateGfwList).mockResolvedValue({ domain_count: 57 });
        render(() => <GfwList />);
        const update = await screen.findByRole('button', { name: copy('gfwlist_update_now') });
        await waitFor(() => expect(update).toBeEnabled());
        fireEvent.click(update);

        await screen.findByText('57');
        expect(addSuccessToast).toHaveBeenCalledWith(copy('gfwlist_updated', { count: 57 }));
    });
});
