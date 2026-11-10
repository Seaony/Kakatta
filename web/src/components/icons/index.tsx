import { useId, type ReactNode } from "react";
import "./icons.css";

export const iconNames = [
  "overview", "tasks", "results", "logs", "accounts", "settings", "ai",
  "prompts", "notifications", "general", "limits", "security", "system",
  "backup", "about", "add", "search", "close", "chevronRight", "chevronDown",
  "more", "play", "pause", "check", "warning", "copy", "external", "download",
  "upload", "star", "delete", "edit", "refresh", "compare", "filter", "menu",
  "sun", "moon", "calendar", "clock", "arrowLeft", "arrowRight",
] as const;

export type IconName = (typeof iconNames)[number];

export type IconProps = {
  name: IconName;
  size?: number;
  title?: string;
  className?: string;
};

function Back({ children }: { children: ReactNode }) {
  return <g data-layer="back" fill="currentColor" fillOpacity="0.08">{children}</g>;
}

function Front({ children }: { children: ReactNode }) {
  return <g data-layer="front" fill="currentColor" fillOpacity="0.16">{children}</g>;
}

const drawings = {
  overview: <>
    <Back><rect x="3" y="3" width="18" height="18" rx="2.2" /></Back>
    <path d="M3 10h18M10 10v11" />
    <Front><rect x="13.5" y="13.5" width="4" height="4" rx="1" /></Front>
  </>,
  tasks: <>
    <Back><path d="M8 5H5.5A1.5 1.5 0 0 0 4 6.5v13A1.5 1.5 0 0 0 5.5 21h13a1.5 1.5 0 0 0 1.5-1.5v-13A1.5 1.5 0 0 0 18.5 5H16" /></Back>
    <Front><rect x="8" y="3" width="8" height="4" rx="1.3" /></Front>
    <path d="m7.5 11 1 1 2-2M13 11h3.5m-9 5 1 1 2-2M13 16h3.5" />
  </>,
  results: <>
    <Back><path d="M7 5V3.5A1.5 1.5 0 0 1 8.5 2h11A1.5 1.5 0 0 1 21 3.5v13a1.5 1.5 0 0 1-1.5 1.5H18" /></Back>
    <Front><rect x="3" y="6" width="14" height="16" rx="2" /></Front>
    <circle cx="7.5" cy="10.5" r="1.1" />
    <path d="m3 18 4-4 3 3 3-3 4 4" />
  </>,
  logs: <>
    <Back><path d="M14.5 3h-9A1.5 1.5 0 0 0 4 4.5v15A1.5 1.5 0 0 0 5.5 21h13a1.5 1.5 0 0 0 1.5-1.5V8.5Z" /></Back>
    <Front><path d="M14 3v4.5A1.5 1.5 0 0 0 15.5 9H20" /></Front>
    <path d="M8 12h8M8 16h6" />
  </>,
  accounts: <>
    <Back><rect x="3" y="4" width="18" height="16" rx="2.2" /></Back>
    <circle cx="9" cy="10" r="2.4" />
    <Front><path d="M4.5 20v-1.5a4.5 4.5 0 0 1 9 0V20" /></Front>
    <path d="M16 9h2m-2 5h2" />
  </>,
  settings: <>
    <Back><path d="m9.5 3-.7 2.7-2 .9-2.5-.7-2 3.4L4.2 11v2l-1.9 1.7 2 3.4 2.5-.7 2 .9.7 2.7h5l.7-2.7 2-.9 2.5.7 2-3.4-1.9-1.7v-2l1.9-1.7-2-3.4-2.5.7-2-.9-.7-2.7Z" /></Back>
    <Front><circle cx="12" cy="12" r="3.5" /></Front>
  </>,
  ai: <>
    <Back><rect x="5" y="5" width="14" height="14" rx="2.2" /></Back>
    <Front><rect x="9" y="9" width="6" height="6" rx="1.2" /></Front>
    <path d="M9 2v3m6-3v3M9 19v3m6-3v3M2 9h3m-3 6h3m14-6h3m-3 6h3" />
  </>,
  prompts: <>
    <Back><path d="M5 3h14a2 2 0 0 1 2 2v11a2 2 0 0 1-2 2h-7l-5 3v-3H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2Z" /></Back>
    <Front><path d="M7 7h3v4H7V9a2 2 0 0 1 2-2m5 0h3v4h-3V9a2 2 0 0 1 2-2" /></Front>
    <path d="M7 14h9" />
  </>,
  notifications: <>
    <Back><path d="M6 10a6 6 0 0 1 12 0v3.5l2 3V18H4v-1.5l2-3Z" /></Back>
    <Front><path d="M6 13.5h12l2 3V18H4v-1.5Z" /></Front>
    <path d="M9.5 21h5M12 2v2" />
  </>,
  general: <>
    <path d="M4 6h3m6 0h7M4 12h9m6 0h1M4 18h3m6 0h7" />
    <Front><circle cx="10" cy="6" r="3" /><circle cx="16" cy="12" r="3" /><circle cx="10" cy="18" r="3" /></Front>
  </>,
  limits: <>
    <Back><path d="M4.9 19a10 10 0 1 1 14.2 0Z" /></Back>
    <Front><path d="m10.5 13.5 5.8-5.8-2.8 7.8Z" /></Front>
    <path d="M4 12h2m12 0h2M12 4v2" />
  </>,
  security: <>
    <Back><path d="m12 2 8 3v6c0 5-3.2 8.5-8 11-4.8-2.5-8-6-8-11V5Z" /></Back>
    <path d="M9 11V9a3 3 0 0 1 6 0v2" />
    <Front><rect x="8" y="11" width="8" height="6" rx="1.3" /></Front>
    <path d="M12 13.5v1" />
  </>,
  system: <>
    <Back><rect x="3" y="3" width="18" height="18" rx="2.2" /></Back>
    <Front><path d="M3 12h18v6.8a2.2 2.2 0 0 1-2.2 2.2H5.2A2.2 2.2 0 0 1 3 18.8Z" /></Front>
    <path d="M7 7.5h1m4 0h5M7 16.5h1m4 0h5" />
  </>,
  backup: <>
    <Back><rect x="4" y="7" width="16" height="14" rx="2" /></Back>
    <Front><rect x="3" y="3" width="18" height="5" rx="1.5" /></Front>
    <path d="M9 12h6m-6 5h6" />
  </>,
  about: <>
    <Back><circle cx="12" cy="12" r="9" /></Back>
    <path d="M12 11v6" />
    <circle cx="12" cy="7.5" r="1" fill="currentColor" stroke="none" />
  </>,
  add: <path d="M12 4v16M4 12h16" />,
  search: <>
    <circle cx="10.5" cy="10.5" r="6.5" />
    <path d="m15.2 15.2 5.3 5.3" />
  </>,
  close: <path d="m6 6 12 12M6 18 18 6" />,
  chevronRight: <path d="m9 5 7 7-7 7" />,
  chevronDown: <path d="m5 9 7 7 7-7" />,
  more: <g fill="currentColor" stroke="none"><circle cx="5" cy="12" r="1.5" /><circle cx="12" cy="12" r="1.5" /><circle cx="19" cy="12" r="1.5" /></g>,
  play: <Front><path d="M7 4.5 20 12 7 19.5Z" /></Front>,
  pause: <Front><rect x="5.5" y="4" width="4" height="16" rx="1" /><rect x="14.5" y="4" width="4" height="16" rx="1" /></Front>,
  check: <path d="m4 12 5 5L20 6" />,
  warning: <>
    <Back><path d="M10.6 3.9a1.6 1.6 0 0 1 2.8 0l8 14.7A1.6 1.6 0 0 1 20 21H4a1.6 1.6 0 0 1-1.4-2.4Z" /></Back>
    <path d="M12 8v5" />
    <circle cx="12" cy="17" r="1" fill="currentColor" stroke="none" />
  </>,
  copy: <>
    <Back><path d="M8 6V4a2 2 0 0 1 2-2h10a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2h-2" /></Back>
    <Front><rect x="2" y="8" width="14" height="14" rx="2" /></Front>
  </>,
  external: <>
    <path d="M12 4H5a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2h13a2 2 0 0 0 2-2v-7M14 3h7v7m0-7L10 14" />
  </>,
  download: <>
    <path d="M12 3v12m-5-5 5 5 5-5" />
    <Front><path d="M4 15v4a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-4" /></Front>
  </>,
  upload: <>
    <path d="M12 15V3m-5 5 5-5 5 5" />
    <Front><path d="M4 15v4a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-4" /></Front>
  </>,
  star: <Front><path d="m12 2 3 6.4 7 1-5 5 1.2 7-6.2-3.3-6.2 3.3 1.2-7-5-5 7-1Z" /></Front>,
  delete: <>
    <Back><path d="m5 7 1 13a1 1 0 0 0 1 1h10a1 1 0 0 0 1-1l1-13" /></Back>
    <Front><path d="M3 7h18M9 7V3h6v4" /></Front>
    <path d="M9 11v6m6-6v6" />
  </>,
  edit: <>
    <Front><path d="m4 16-1 5 5-1L21 7a2.8 2.8 0 0 0-4-4Z" /></Front>
    <path d="m14 6 4 4M4 16l4 4" />
  </>,
  refresh: <>
    <path d="M20.5 9a9 9 0 0 0-15-3L3 9m0-6v6h6m-5.5 6a9 9 0 0 0 15 3l2.5-3m0 6v-6h-6" />
  </>,
  compare: <>
    <Back><rect x="2" y="4" width="8" height="16" rx="1.7" /></Back>
    <Front><rect x="14" y="4" width="8" height="16" rx="1.7" /></Front>
    <path d="M5 8h2m10 0h2M5 12h2m10 0h2M5 16h2m10 0h2" />
  </>,
  filter: <>
    <Front><path d="M3 4h18l-7 8v7l-4 2v-9Z" /></Front>
  </>,
  menu: <path d="M4 6h16M4 12h16M4 18h16" />,
  sun: <>
    <Front><circle cx="12" cy="12" r="4" /></Front>
    <path d="M12 2v2m0 16v2M2 12h2m16 0h2M4.9 4.9l1.4 1.4m11.4 11.4 1.4 1.4M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" />
  </>,
  moon: <Front><path d="M20.8 14.6A9.2 9.2 0 0 1 9.4 3.2a9.2 9.2 0 1 0 11.4 11.4Z" /></Front>,
  calendar: <>
    <Back><rect x="3" y="5" width="18" height="16" rx="2.2" /></Back>
    <Front><path d="M3 9V7.2A2.2 2.2 0 0 1 5.2 5h13.6A2.2 2.2 0 0 1 21 7.2V9Z" /></Front>
    <path d="M7 2v5m10-5v5M7 13h3m4 0h3M7 17h3m4 0h3" />
  </>,
  clock: <>
    <Back><circle cx="12" cy="12" r="9" /></Back>
    <path d="M12 6v6l4 2" />
  </>,
  arrowLeft: <path d="M21 12H3m7-7-7 7 7 7" />,
  arrowRight: <path d="M3 12h18m-7-7 7 7-7 7" />,
} satisfies Record<IconName, ReactNode>;

export function Icon({ name, size = 20, title, className }: IconProps) {
  const titleId = useId();
  const labelled = Boolean(title?.trim());

  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      viewBox="0 0 24 24"
      width={size}
      height={size}
      fill="none"
      stroke="currentColor"
      strokeWidth="1.7"
      strokeLinecap="round"
      strokeLinejoin="round"
      focusable="false"
      aria-hidden={labelled ? undefined : true}
      role={labelled ? "img" : undefined}
      aria-labelledby={labelled ? titleId : undefined}
      className={["kakatta-icon inline-block shrink-0 align-middle", className].filter(Boolean).join(" ")}
      data-icon={name}
    >
      {labelled && <title id={titleId}>{title}</title>}
      <g data-icon-body="">{drawings[name]}</g>
    </svg>
  );
}
