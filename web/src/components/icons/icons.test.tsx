import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { render, screen } from "@testing-library/react";
import { renderToStaticMarkup } from "react-dom/server";
import { Icon, iconNames } from ".";

describe("功能图标", () => {
  test("全部名称在 20 和 24 像素下输出合法、独立的 SVG", () => {
    expect(new Set(iconNames).size).toBe(42);
    const parser = new DOMParser();
    for (const name of iconNames) {
      for (const size of [20, 24]) {
        const markup = renderToStaticMarkup(<Icon name={name} size={size} />);
        const parsed = parser.parseFromString(markup, "image/svg+xml");
        expect(parsed.querySelector("parsererror")).toBeNull();
        const svg = parsed.documentElement;
        expect(svg.localName).toBe("svg");
        expect(svg.namespaceURI).toBe("http://www.w3.org/2000/svg");
        expect(svg.getAttribute("viewBox")).toBe("0 0 24 24");
        expect(svg.getAttribute("width")).toBe(String(size));
        expect(svg.getAttribute("height")).toBe(String(size));
        expect(svg.getAttribute("stroke")).toBe("currentColor");
        expect(svg.getAttribute("stroke-width")).toBe("1.7");
        expect(svg.querySelector("path, rect, circle")).not.toBeNull();
        expect(svg.querySelector("script, use, image, foreignObject, animate, animateTransform")).toBeNull();
        expect(svg.querySelector("[href], [style]")).toBeNull();
        expect(markup).not.toMatch(/NaN|undefined|url\(|#[\da-f]{3,8}\b/i);
        for (const path of svg.querySelectorAll("path")) {
          const data = path.getAttribute("d");
          expect(data).toMatch(/^[Mm][\d\s.,+\-MmLlHhVvCcSsQqTtAaZz]+$/);
        }
        for (const element of svg.querySelectorAll("[fill], [stroke]")) {
          for (const attribute of ["fill", "stroke"]) {
            const value = element.getAttribute(attribute);
            if (value !== null) expect(["none", "currentColor"]).toContain(value);
          }
        }
      }
    }
  });

  test("默认装饰图标不改变按钮名称，也不进入键盘顺序", () => {
    const { container } = render(<button><Icon name="add" />新建任务</button>);
    expect(screen.getByRole("button", { name: "新建任务" })).not.toBeNull();
    expect(screen.queryByRole("img")).toBeNull();
    const svg = container.querySelector("svg");
    expect(svg?.getAttribute("aria-hidden")).toBe("true");
    expect(svg?.getAttribute("focusable")).toBe("false");
    expect(svg?.getAttribute("tabindex")).toBeNull();
    expect(svg?.getAttribute("width")).toBe("20");
    expect(svg?.querySelector("title")).toBeNull();
  });

  test("显式标题提供名称，同页实例的标题引用互不冲突", () => {
    render(<><Icon name="security" title="账号安全" /><Icon name="security" title="Account security" /></>);
    const chinese = screen.getByRole("img", { name: "账号安全" });
    const english = screen.getByRole("img", { name: "Account security" });
    expect(chinese.getAttribute("aria-hidden")).toBeNull();
    expect(chinese.getAttribute("aria-labelledby")).not.toBe(english.getAttribute("aria-labelledby"));
    for (const svg of [chinese, english]) {
      const title = svg.querySelector("title");
      if (title === null) throw new Error("图标缺少标题");
      expect(svg.getAttribute("aria-labelledby")).toBe(title.id);
    }
  });

  test("标题安全转义，去掉标题后恢复装饰状态，空白标题不暴露空名称", () => {
    const { container, rerender } = render(<Icon name="warning" title={'<script>alert("test")</script>'} className="text-warning" />);
    expect(screen.getByRole("img", { name: '<script>alert("test")</script>' })).not.toBeNull();
    expect(container.querySelector("script")).toBeNull();
    expect(container.querySelector("svg")?.classList.contains("text-warning")).toBe(true);
    rerender(<Icon name="warning" title="   " />);
    expect(screen.queryByRole("img")).toBeNull();
    expect(container.querySelector("title")).toBeNull();
    expect(container.querySelector("svg")?.getAttribute("aria-labelledby")).toBeNull();
    rerender(<Icon name="warning" />);
    expect(container.querySelector("svg")?.getAttribute("aria-hidden")).toBe("true");
  });

  test("形变只在允许动态效果的鼠标悬停中启用，高频操作没有动画规则", () => {
    const css = readFileSync(new URL("./icons.css", import.meta.url), "utf8");
    expect(css).toMatch(/^@media \(hover: hover\) and \(prefers-reduced-motion: no-preference\)\s*\{/);
    expect(css.match(/\[data-icon="([^"]+)"\]/g)).toEqual([
      '[data-icon="settings"]', '[data-icon="settings"]',
    ]);
    expect(css).toContain(":where(a, button):hover");
    expect(css).not.toMatch(/animation\s*:|:focus|transition\s*:\s*all/);
  });
});
