import type { ComponentPropsWithoutRef } from "react";
import { classes } from "./styles";

export type SkeletonProps = Omit<ComponentPropsWithoutRef<"div">, "children" | "aria-hidden">;

export function Skeleton({ className, ...props }: SkeletonProps) {
  return <div {...props} aria-hidden="true" className={classes("h-lg rounded-minimum bg-control", className)} />;
}
