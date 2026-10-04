import * as React from "react";
import { Dialog as Primitive } from "radix-ui";
import { X } from "lucide-react";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";

export const Dialog = Primitive.Root;
export const DialogTitle = Primitive.Title;
export const DialogDescription = Primitive.Description;

export function DialogContent({
  className,
  children,
  ...props
}: React.ComponentProps<typeof Primitive.Content>) {
  return (
    <Primitive.Portal>
      <Primitive.Overlay className="fixed inset-0 z-50 bg-black/65 backdrop-blur-sm data-[state=open]:animate-in data-[state=open]:fade-in-0" />
      <Primitive.Content
        className={cn(
          "fixed top-1/2 left-1/2 z-50 max-h-[90svh] w-[calc(100%_-_2rem)] max-w-2xl -translate-x-1/2 -translate-y-1/2 overflow-y-auto rounded-xl border bg-background p-5 shadow-2xl outline-none sm:p-7",
          className,
        )}
        {...props}
      >
        {children}
        <Primitive.Close asChild>
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            className="absolute top-4 right-4"
            aria-label="Close"
          >
            <X aria-hidden="true" />
          </Button>
        </Primitive.Close>
      </Primitive.Content>
    </Primitive.Portal>
  );
}
