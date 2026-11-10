import { describe, expect, mock, test } from "bun:test";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createRef, useState } from "react";
import {
  ActionMenu, Button, Checkbox, CopyButton, DataTable, FormRow, FormSection,
  InlineMessage, PageHeader, Pagination, RadioGroup, SecretField, SelectField,
  Switch, Tabs,
} from "./index";

describe("表单控件", () => {
  test("原生下拉保留表单值并关联错误，禁用选项不可选择", async () => {
    const changed = mock((value: string) => value);
    function Example() {
      const [value, setValue] = useState("all");
      return <form data-testid="form"><p id="source">固定账号</p><SelectField id="account" name="account" label="账号" hint="先绑定账号" error="账号暂不可用" aria-describedby="source" value={value} onChange={(event) => setValue(event.currentTarget.value)} onValueChange={changed} options={[{ value: "all", label: "自动选择" }, { value: "first", label: "备用账号" }, { value: "blocked", label: "已暂停", disabled: true }]} /></form>;
    }
    render(<Example />);
    const select = screen.getByRole("combobox", { name: "账号" });
    expect(select.getAttribute("aria-describedby")).toBe("source account-hint account-error");
    expect(select.getAttribute("aria-invalid")).toBe("true");
    await userEvent.setup().selectOptions(select, "first");
    expect(changed).toHaveBeenLastCalledWith("first");
    expect(new FormData(screen.getByTestId("form") as HTMLFormElement).get("account")).toBe("first");
    await userEvent.setup().selectOptions(select, "blocked");
    expect((select as HTMLSelectElement).value).toBe("first");
  });

  test("复选框整行标签可点击，支持部分选中、键盘和外部引用", async () => {
    const ref = createRef<HTMLInputElement>();
    function Example() {
      const [checked, setChecked] = useState(false);
      return <Checkbox ref={ref} label="选择全部" checked={checked} indeterminate={!checked} onCheckedChange={setChecked} hint="只选择当前批次" />;
    }
    render(<Example />);
    const checkbox = screen.getByRole<HTMLInputElement>("checkbox", { name: "选择全部" });
    expect(ref.current).toBe(checkbox);
    expect(ref.current?.indeterminate).toBe(true);
    expect(checkbox.getAttribute("aria-checked")).toBe("mixed");
    await userEvent.setup().click(screen.getByText("选择全部"));
    expect(ref.current?.checked).toBe(true);
    expect(ref.current?.indeterminate).toBe(false);
    checkbox.focus();
    await userEvent.setup().keyboard("[Space]");
    expect(ref.current?.checked).toBe(false);
  });

  test("禁用复选框保持原值，不调用任何变更回调", async () => {
    const changed = mock(() => {});
    render(<Checkbox label="启用任务" checked disabled onChange={changed} onCheckedChange={changed} error="账号需要验证" />);
    await userEvent.setup().click(screen.getByText("启用任务"));
    expect((screen.getByRole("checkbox") as HTMLInputElement).checked).toBe(true);
    expect(screen.getByRole("checkbox").getAttribute("aria-invalid")).toBe("true");
    expect(changed).not.toHaveBeenCalled();
  });

  test("开关用标签命名，Space切换并把值写入表单", async () => {
    function Example() {
      const [checked, setChecked] = useState(false);
      return <form data-testid="form"><Switch id="paused" name="paused" value="yes" label="暂停监控" hint="之后可恢复" checked={checked} onCheckedChange={setChecked} /></form>;
    }
    render(<Example />);
    const control = screen.getByRole("switch", { name: "暂停监控" });
    expect(control.getAttribute("aria-checked")).toBe("false");
    control.focus();
    await userEvent.setup().keyboard("[Space]");
    expect(control.getAttribute("aria-checked")).toBe("true");
    expect(new FormData(screen.getByTestId("form") as HTMLFormElement).get("paused")).toBe("yes");
    await userEvent.setup().click(screen.getByText("暂停监控"));
    expect(control.getAttribute("aria-checked")).toBe("false");
  });

  test("单选方向键跳过禁用项，错误与选项说明均保留", async () => {
    const changed = mock((value: string) => value);
    render(<RadioGroup id="range" label="日期范围" defaultValue="today" onValueChange={changed} orientation="horizontal" error="请检查范围" options={[{ value: "today", label: "今天", description: "从零点开始" }, { value: "week", label: "近七天", disabled: true }, { value: "month", label: "近三十天" }]} />);
    const group = screen.getByRole("radiogroup", { name: "日期范围" });
    expect(group.getAttribute("aria-describedby")).toBe("range-error");
    const today = screen.getByRole("radio", { name: "今天 从零点开始" });
    act(() => today.focus());
    const user = userEvent.setup();
    await user.keyboard("[ArrowRight>]");
    await waitFor(() => expect(screen.getByRole("radio", { name: "近三十天" }).getAttribute("aria-checked")).toBe("true"));
    await user.keyboard("[/ArrowRight]");
    expect(document.activeElement).toBe(screen.getByRole("radio", { name: "近三十天" }));
    expect(changed).toHaveBeenLastCalledWith("month");
  });

  test("秘密字段显隐不改输入，保留自动补全、错误与ref", async () => {
    const ref = createRef<HTMLInputElement>();
    function Example() {
      const [value, setValue] = useState("初始密钥");
      return <SecretField id="key" ref={ref} label="服务密钥" autoComplete="new-password" error="请确认密钥" value={value} onChange={(event) => setValue(event.currentTarget.value)} showLabel="显示密钥" hideLabel="隐藏密钥" />;
    }
    render(<Example />);
    const input = screen.getByLabelText("服务密钥") as HTMLInputElement;
    expect(ref.current).toBe(input);
    expect(input.type).toBe("password");
    expect(input.autocomplete).toBe("new-password");
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "显示密钥" }));
    expect(input.type).toBe("text");
    expect(input.value).toBe("初始密钥");
    expect(screen.getByRole("button", { name: "隐藏密钥" }).getAttribute("aria-controls")).toBe("key");
    await user.type(input, "后缀");
    await user.click(screen.getByRole("button", { name: "隐藏密钥" }));
    expect(input.type).toBe("password");
    expect(input.value).toBe("初始密钥后缀");
    expect(input.getAttribute("aria-describedby")).toBe("key-error");
  });
});

describe("菜单与页签", () => {
  test("页签方向键切换内容，跳过禁用项并维护关联", async () => {
    render(<Tabs label="结果状态" items={[{ value: "ready", label: "已推荐", content: <p>推荐记录</p> }, { value: "waiting", label: "等待分析", disabled: true, content: <p>等待记录</p> }, { value: "hidden", label: "已隐藏", content: <p>隐藏记录</p> }]} />);
    const first = screen.getByRole("tab", { name: "已推荐" });
    act(() => first.focus());
    await userEvent.setup().keyboard("[ArrowRight]");
    const hidden = screen.getByRole("tab", { name: "已隐藏" });
    await waitFor(() => expect(hidden.getAttribute("aria-selected")).toBe("true"));
    expect(document.activeElement).toBe(hidden);
    const panel = screen.getByRole("tabpanel", { name: "已隐藏" });
    expect(panel.id).toBe(hidden.getAttribute("aria-controls") ?? "");
    expect(panel.textContent).toBe("隐藏记录");
    expect(screen.queryByText("推荐记录")).toBeNull();
  });

  test("菜单键盘打开、跳过禁用项、关闭后归还焦点", async () => {
    const selected = mock(() => {});
    const forbidden = mock(() => {});
    render(<ActionMenu label="任务操作" items={[{ id: "blocked", label: "暂不可编辑", disabled: true, onSelect: forbidden }, { id: "run", label: "立即运行", onSelect: selected }, { id: "remove", label: "删除任务", destructive: true, onSelect: selected }]} />);
    const trigger = screen.getByRole("button", { name: "任务操作" });
    trigger.focus();
    const user = userEvent.setup();
    await user.keyboard("[Enter]");
    const menu = screen.getByRole("menu", { name: "任务操作" });
    const run = within(menu).getByRole("menuitem", { name: "立即运行" });
    await waitFor(() => expect(document.activeElement).toBe(run));
    await user.keyboard("[Enter]");
    await waitFor(() => expect(screen.queryByRole("menu")).toBeNull());
    expect(selected).toHaveBeenCalledTimes(1);
    expect(forbidden).not.toHaveBeenCalled();
    await waitFor(() => expect(document.activeElement).toBe(trigger));
    await user.keyboard("[Enter]");
    await user.keyboard("[Escape]");
    await waitFor(() => expect(document.activeElement).toBe(trigger));
  });
});

describe("复制与分页", () => {
  test("实际复制完成才显示成功，未完成时阻止重复动作", async () => {
    const original = Object.getOwnPropertyDescriptor(navigator, "clipboard");
    let finish: (() => void) | undefined;
    const write = mock(() => new Promise<void>((resolve) => { finish = resolve; }));
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: write } });
    try {
      const copied = mock(() => {});
      render(<CopyButton text="https://example.invalid/item/1" label="复制链接" copiedLabel="已复制" errorLabel="复制失败，请选择文本" onCopied={copied} />);
      const button = screen.getByRole("button", { name: "复制链接" });
      fireEvent.click(button);
      expect(button.getAttribute("aria-busy")).toBe("true");
      fireEvent.click(button);
      expect(write).toHaveBeenCalledTimes(1);
      expect(screen.queryByText("已复制")).toBeNull();
      finish?.();
      await waitFor(() => expect(screen.getByRole("button", { name: "已复制" })).toBeTruthy());
      expect(write).toHaveBeenCalledWith("https://example.invalid/item/1");
      expect(copied).toHaveBeenCalledTimes(1);
    } finally {
      if (original) Object.defineProperty(navigator, "clipboard", original);
      else Reflect.deleteProperty(navigator, "clipboard");
    }
  });

  test("复制失败保留完整原文，允许手动选取和再次复制", async () => {
    const original = Object.getOwnPropertyDescriptor(navigator, "clipboard");
    const write = mock(() => Promise.reject(new Error("denied")));
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: write } });
    try {
      const copied = mock(() => {});
      render(<CopyButton text={"一次令牌\n完整第二行"} label="复制令牌" copiedLabel="已复制" errorLabel="复制失败，请选择文本" onCopied={copied} />);
      fireEvent.click(screen.getByRole("button", { name: "复制令牌" }));
      await waitFor(() => expect(screen.getByRole("alert").textContent).toBe("复制失败，请选择文本"));
      const fallback = screen.getByRole("textbox", { name: "复制失败，请选择文本" }) as HTMLTextAreaElement;
      expect(fallback.value).toBe("一次令牌\n完整第二行");
      expect(fallback.readOnly).toBe(true);
      fireEvent.focus(fallback);
      expect(fallback.selectionStart).toBe(0);
      expect(fallback.selectionEnd).toBe(fallback.value.length);
      expect(copied).not.toHaveBeenCalled();
      expect((screen.getByRole("button", { name: "复制令牌" }) as HTMLButtonElement).disabled).toBe(false);
    } finally {
      if (original) Object.defineProperty(navigator, "clipboard", original);
      else Reflect.deleteProperty(navigator, "clipboard");
    }
  });

  test("复制等待期间原文改变时，不把旧内容的完成提示用于新内容", async () => {
    const original = Object.getOwnPropertyDescriptor(navigator, "clipboard");
    let finish: (() => void) | undefined;
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: () => new Promise<void>((resolve) => { finish = resolve; }) } });
    try {
      const copied = mock(() => {});
      const { rerender } = render(<CopyButton text="旧令牌" label="复制令牌" copiedLabel="已复制" errorLabel="请手动复制" onCopied={copied} />);
      fireEvent.click(screen.getByRole("button", { name: "复制令牌" }));
      rerender(<CopyButton text="新令牌" label="复制令牌" copiedLabel="已复制" errorLabel="请手动复制" onCopied={copied} />);
      await act(async () => { finish?.(); });
      expect(copied).not.toHaveBeenCalled();
      expect(screen.queryByText("已复制")).toBeNull();
      expect((screen.getByRole("button", { name: "复制令牌" }) as HTMLButtonElement).disabled).toBe(false);
    } finally {
      if (original) Object.defineProperty(navigator, "clipboard", original);
      else Reflect.deleteProperty(navigator, "clipboard");
    }
  });

  test("浏览器没有剪贴板权限接口时仍提供完整可选文本", async () => {
    const original = Object.getOwnPropertyDescriptor(navigator, "clipboard");
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: undefined });
    try {
      render(<CopyButton text="待手动复制的内容" label="复制" copiedLabel="已复制" errorLabel="浏览器不允许复制，请手动选择" />);
      fireEvent.click(screen.getByRole("button", { name: "复制" }));
      await waitFor(() => expect(screen.getByRole("textbox", { name: "浏览器不允许复制，请手动选择" })).toBeTruthy());
      expect((screen.getByRole("textbox") as HTMLTextAreaElement).value).toBe("待手动复制的内容");
    } finally {
      if (original) Object.defineProperty(navigator, "clipboard", original);
      else Reflect.deleteProperty(navigator, "clipboard");
    }
  });

  test("分页不推算未知总页数，首批和末批正确禁用动作", async () => {
    const change = mock((page: number) => page);
    const { rerender } = render(<Pagination page={1} hasNextPage onPageChange={change} previousLabel="上一批" nextLabel="下一批" pageLabel="当前显示25条" />);
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "上一批" }));
    expect(change).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "下一批" }));
    expect(change).toHaveBeenLastCalledWith(2);
    rerender(<Pagination page={2} hasNextPage={false} onPageChange={change} previousLabel="上一批" nextLabel="下一批" pageLabel="当前显示8条" />);
    expect((screen.getByRole("button", { name: "下一批" }) as HTMLButtonElement).disabled).toBe(true);
    await user.click(screen.getByRole("button", { name: "上一批" }));
    expect(change).toHaveBeenLastCalledWith(1);
    expect(screen.getByRole("status").textContent).toBe("当前显示8条");
  });
});

describe("信息结构", () => {
  test("表格保持表头、零值和原文，空状态不伪装数据", () => {
    const columns = [{ key: "name", header: "任务", cell: (row: { id: number; name: string; count: number | null }) => row.name }, { key: "count", header: "数量", align: "right" as const, cell: (row: { id: number; name: string; count: number | null }) => row.count === null ? "未知" : row.count }];
    const { rerender } = render(<DataTable label="任务统计" columns={columns} rows={[{ id: 1, name: "含有很长商品规格的任务名称", count: 0 }, { id: 2, name: "尚未运行", count: null }]} rowKey={(row) => row.id} empty="暂无任务" busy />);
    const region = screen.getByRole("region", { name: "任务统计" });
    expect(region.getAttribute("aria-busy")).toBe("true");
    expect(region.getAttribute("tabindex")).toBe("0");
    expect(screen.getByRole("columnheader", { name: "数量" }).getAttribute("scope")).toBe("col");
    expect(screen.getByRole("cell", { name: "0" })).toBeTruthy();
    expect(screen.getByRole("cell", { name: "未知" })).toBeTruthy();
    rerender(<DataTable label="任务统计" columns={columns} rows={[]} rowKey={(row) => row.id} empty="暂无任务" />);
    expect(screen.getByRole("cell", { name: "暂无任务" }).getAttribute("colspan")).toBe("2");
    expect(screen.queryByRole("cell", { name: "0" })).toBeNull();
  });

  test("页面与表单标题使用语义层级，说明不抢占控件名称", () => {
    render(<><PageHeader title="抓取与限制" description="频率配置" actions={<Button>保存全部</Button>} /><FormSection title="每日额度" description="调整前检查推荐范围"><FormRow label="每日数量" htmlFor="amount" description="包含所有任务"><input id="amount" /></FormRow></FormSection><InlineMessage tone="danger" title="保存失败" action={<Button>重试保存</Button>}>输入内容已保留</InlineMessage></>);
    expect(screen.getByRole("heading", { level: 1, name: "抓取与限制" })).toBeTruthy();
    expect(screen.getByRole("region", { name: "每日额度" })).toBeTruthy();
    expect(screen.getByRole("textbox", { name: "每日数量" })).toBeTruthy();
    expect(screen.getByRole("alert").textContent).toContain("输入内容已保留");
    expect(screen.getByRole("button", { name: "重试保存" })).toBeTruthy();
  });
});
