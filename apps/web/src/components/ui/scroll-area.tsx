import type { ComponentProps } from "react";
import { ScrollArea as Primitive } from "radix-ui";
import { cn } from "@/lib/utils";
export function ScrollArea({
  className,
  children,
  ...props
}: ComponentProps<typeof Primitive.Root>) {
  return (
    <Primitive.Root className={cn("relative", className)} {...props}>
      <Primitive.Viewport
        data-slot="scroll-area-viewport"
        className="size-full rounded-[inherit] outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        {children}
      </Primitive.Viewport>
      <ScrollBar />
      <Primitive.Corner />
    </Primitive.Root>
  );
}
export function ScrollBar({
  className,
  orientation = "vertical",
  ...props
}: ComponentProps<typeof Primitive.Scrollbar>) {
  return (
    <Primitive.Scrollbar
      orientation={orientation}
      className={cn(
        "flex touch-none select-none p-px",
        orientation === "vertical" ? "h-full w-2.5" : "h-2.5 flex-col",
        className,
      )}
      {...props}
    >
      <Primitive.Thumb className="relative flex-1 rounded-full bg-border" />
    </Primitive.Scrollbar>
  );
}
