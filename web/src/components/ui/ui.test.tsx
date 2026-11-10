import { describe, expect, mock, test } from "bun:test";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { Button } from "./button";
import { ConfirmDialog, Dialog } from "./dialog";
import { Popover } from "./popover";
import { Skeleton } from "./skeleton";
import { StatePanel } from "./state-panel";
import { StatusBadge } from "./status-badge";
import { TextAreaField, TextField } from "./text-field";

describe("基础控件", () => {
  test("按钮默认不提交表单，处理中阻止重复点击且保留文字", async () => {
    const submit = mock(() => {});
    const action = mock(() => {});
    function Example() {
      const [busy, setBusy] = useState(false);
      return <form onSubmit={submit}><Button busy={busy} onClick={() => { action(); setBusy(true); }}>保存</Button></form>;
    }
    render(<Example />);
    const user = userEvent.setup();
    const button = screen.getByRole("button", { name: "保存" });
    expect(button.getAttribute("type")).toBe("button");
    await user.dblClick(button);
    expect(action).toHaveBeenCalledTimes(1);
    expect(submit).not.toHaveBeenCalled();
    expect(button.getAttribute("aria-busy")).toBe("true");
    expect((button as HTMLButtonElement).disabled).toBe(true);
  });

  test("字段关联标签、说明和错误，并保留外部描述", () => {
    const { rerender } = render(<><p id="existing">已有说明</p><TextField id="query" label="关键词" hint="可以输入中文" error="请填写关键词" aria-describedby="existing" required /></>);
    const input = screen.getByRole("textbox", { name: "关键词" });
    expect(input.id).toBe("query");
    expect(input.getAttribute("aria-describedby")).toBe("existing query-hint query-error");
    expect(input.getAttribute("aria-invalid")).toBe("true");
    expect(screen.getByRole("alert").textContent).toBe("请填写关键词");
    expect((input as HTMLInputElement).required).toBe(true);
    rerender(<TextField id="query" label="关键词" hint="可以输入中文" />);
    const updated = screen.getByRole("textbox", { name: "关键词" });
    expect(updated.getAttribute("aria-describedby")).toBe("query-hint");
    expect(updated.getAttribute("aria-invalid")).toBeNull();
  });

  test("中文组合输入不提交表单，也不改写或重复插入文字", () => {
    const submit = mock((event: React.FormEvent) => event.preventDefault());
    const composed = mock(() => {});
    function Example() {
      const [value, setValue] = useState("");
      return <form onSubmit={submit}><TextAreaField label="需求" value={value} onChange={(event) => setValue(event.target.value)} onCompositionEnd={composed} /></form>;
    }
    render(<Example />);
    const input = screen.getByRole("textbox", { name: "需求" });
    fireEvent.compositionStart(input);
    fireEvent.change(input, { target: { value: "相机" } });
    fireEvent.keyDown(input, { key: "Enter", code: "Enter", isComposing: true });
    fireEvent.compositionEnd(input, { data: "相机" });
    expect((input as HTMLTextAreaElement).value).toBe("相机");
    expect(composed).toHaveBeenCalledTimes(1);
    expect(submit).not.toHaveBeenCalled();
  });

  test("状态提供文字与恢复操作，骨架不进入无障碍内容", async () => {
    const retry = mock(() => {});
    const { rerender } = render(<StatePanel kind="pending" label="正在读取"><Skeleton data-testid="skeleton" /></StatePanel>);
    expect(screen.getByRole("status").textContent).toBe("正在读取");
    expect(screen.getByLabelText("正在读取").getAttribute("aria-busy")).toBe("true");
    expect(screen.getByTestId("skeleton").getAttribute("aria-hidden")).toBe("true");
    rerender(<StatePanel kind="error" title="读取失败" description="请检查网络" retry={{ label: "重试", run: retry }} />);
    expect(screen.getByRole("alert").textContent).toContain("请检查网络");
    await userEvent.setup().click(screen.getByRole("button", { name: "重试" }));
    expect(retry).toHaveBeenCalledTimes(1);
    rerender(<StatusBadge tone="warning">需要验证</StatusBadge>);
    expect(screen.getByText("需要验证").textContent).toBe("需要验证");
  });
});

describe("弹层键盘与焦点", () => {
  test("Dialog 按 Enter 打开，Tab 留在内部，Escape 关闭后归还焦点", async () => {
    function Example() {
      const [open, setOpen] = useState(false);
      return <Dialog open={open} onOpenChange={setOpen} title="编辑任务" description="修改名称" closeLabel="关闭" trigger={<Button>编辑</Button>}><TextField label="名称" /><Button>保存</Button></Dialog>;
    }
    render(<Example />);
    const user = userEvent.setup();
    const trigger = screen.getByRole("button", { name: "编辑" });
    trigger.focus();
    await user.keyboard("[Enter]");
    const dialog = screen.getByRole("dialog", { name: "编辑任务" });
    const close = within(dialog).getByRole("button", { name: "关闭" });
    await waitFor(() => expect(document.activeElement).toBe(close));
    await user.tab({ shift: true });
    expect(document.activeElement).toBe(within(dialog).getByRole("button", { name: "保存" }));
    await user.tab();
    expect(document.activeElement).toBe(close);
    await user.keyboard("[Escape]");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await waitFor(() => expect(document.activeElement).toBe(trigger));
  });

  test("外部受控打开 Dialog 也能归还原焦点", async () => {
    function Example() {
      const [open, setOpen] = useState(false);
      return <><Button onClick={() => setOpen(true)}>外部打开</Button><Dialog open={open} onOpenChange={setOpen} title="详情" closeLabel="关闭"><p>内容</p></Dialog></>;
    }
    render(<Example />);
    const user = userEvent.setup();
    const trigger = screen.getByRole("button", { name: "外部打开" });
    await user.click(trigger);
    await user.click(screen.getByRole("button", { name: "关闭" }));
    await waitFor(() => expect(document.activeElement).toBe(trigger));
  });

  test("确认操作不会自动关闭，错误留在弹层，处理中不重复确认", async () => {
    const confirm = mock(() => {});
    function Example() {
      const [open, setOpen] = useState(false);
      const [pending, setPending] = useState(false);
      const [error, setError] = useState("");
      return <ConfirmDialog open={open} onOpenChange={setOpen} title="删除任务" description="删除后无法撤销" confirmLabel="删除" cancelLabel="取消" pending={pending} error={error} trigger={<Button>删除任务</Button>} onConfirm={() => { confirm(); setPending(true); setError("暂时无法删除，请稍后重试"); }} />;
    }
    render(<Example />);
    const user = userEvent.setup();
    const trigger = screen.getByRole("button", { name: "删除任务" });
    await user.click(trigger);
    const dialog = screen.getByRole("alertdialog", { name: "删除任务" });
    const cancel = within(dialog).getByRole("button", { name: "取消" });
    await waitFor(() => expect(document.activeElement).toBe(cancel));
    await user.dblClick(within(dialog).getByRole("button", { name: "删除" }));
    expect(confirm).toHaveBeenCalledTimes(1);
    expect(within(dialog).getByRole("alert").textContent).toBe("暂时无法删除，请稍后重试");
    expect(screen.getByRole("alertdialog")).toBe(dialog);
    await user.click(cancel);
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
    await waitFor(() => expect(document.activeElement).toBe(trigger));
  });

  test("Popover 通过键盘打开和关闭，Portal 内容有名称并归还焦点", async () => {
    function Example() {
      const [open, setOpen] = useState(false);
      return <Popover open={open} onOpenChange={setOpen} label="筛选条件" trigger={<Button>筛选</Button>}><TextField label="关键词" /></Popover>;
    }
    render(<Example />);
    const user = userEvent.setup();
    const trigger = screen.getByRole("button", { name: "筛选" });
    trigger.focus();
    await user.keyboard("[Enter]");
    const dialog = screen.getByRole("dialog", { name: "筛选条件" });
    expect(document.activeElement).toBe(within(dialog).getByRole("textbox", { name: "关键词" }));
    await user.keyboard("[Escape]");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await waitFor(() => expect(document.activeElement).toBe(trigger));
  });
});
