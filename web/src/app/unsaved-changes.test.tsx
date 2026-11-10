import { expect, test } from 'bun:test';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider, Link, Outlet } from '@tanstack/react-router';
import { useState } from 'react';
import { createI18n, I18nProvider } from '../i18n';
import { UnsavedChangesProvider, useUnsavedChanges } from './unsaved-changes';

function EditablePage() {
  const [value, setValue] = useState('');
  useUnsavedChanges(value.length > 0);
  return <><input aria-label="未保存内容" value={value} onChange={(event) => setValue(event.currentTarget.value)} /><Link to="/overview">去概览</Link></>;
}

test('跨页未保存确认只有一份，取消保留输入，明确放弃才离开', async () => {
  const root = createRootRoute({ component: () => <UnsavedChangesProvider><Outlet /></UnsavedChangesProvider> });
  const editing = createRoute({ getParentRoute: () => root, path: '/tasks', component: EditablePage });
  const target = createRoute({ getParentRoute: () => root, path: '/overview', component: () => <h1>目标页</h1> });
  const router = createRouter({ routeTree: root.addChildren([editing, target]), history: createMemoryHistory({ initialEntries: ['/tasks'] }) });
  await router.load();
  const view = render(<I18nProvider instance={createI18n('zh-CN')}><RouterProvider router={router} /></I18nProvider>);
  try {
    const input = await screen.findByRole<HTMLInputElement>('textbox', { name: '未保存内容' });
    await userEvent.type(input, '保留');
    await userEvent.click(screen.getByRole('link', { name: '去概览' }));
    const dialog = await screen.findByRole('alertdialog', { name: '放弃未保存的修改？' });
    expect(screen.getAllByRole('alertdialog')).toHaveLength(1);
    await userEvent.click(within(dialog).getByRole('button', { name: '取消' }));
    expect(input.value).toBe('保留');
    expect(router.state.location.pathname).toBe('/tasks');
    await userEvent.click(screen.getByRole('link', { name: '去概览' }));
    const next = await screen.findByRole('alertdialog');
    await userEvent.click(within(next).getByRole('button', { name: '放弃修改并离开' }));
    await waitFor(() => expect(screen.getByRole('heading', { name: '目标页' })).toBeTruthy());
    expect(router.state.location.pathname).toBe('/overview');
  } finally { view.unmount(); }
});
