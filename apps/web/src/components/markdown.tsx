import {
  Children,
  isValidElement,
  useState,
  type ComponentProps,
  type ReactNode,
} from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import rehypeHighlight from "rehype-highlight";
import { Check, Copy } from "lucide-react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

export function CopyText({
  text,
  label = "Copy",
  className,
}: {
  text: string;
  label?: string;
  className?: string;
}) {
  const [state, setState] = useState<"idle" | "copied" | "failed">("idle");
  return (
    <Button
      type="button"
      size="sm"
      variant="ghost"
      className={className}
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(text);
          setState("copied");
        } catch {
          setState("failed");
        }
      }}
      aria-label={state === "copied" ? "Copied" : label}
    >
      {state === "copied" ? (
        <Check aria-hidden="true" />
      ) : (
        <Copy aria-hidden="true" />
      )}
      <span aria-live="polite">
        {state === "copied"
          ? "Copied"
          : state === "failed"
            ? "Copy failed — retry"
            : label}
      </span>
    </Button>
  );
}

function plainText(children: ReactNode): string {
  return Children.toArray(children)
    .map((child) => {
      if (typeof child === "string" || typeof child === "number")
        return String(child);
      return isValidElement<{ children?: ReactNode }>(child)
        ? plainText(child.props.children)
        : "";
    })
    .join("");
}

function CodeBlock({ children }: ComponentProps<"pre">) {
  const child = Children.toArray(children)[0];
  const language = isValidElement<{ className?: string }>(child)
    ? /language-([^\s]+)/.exec(child.props.className ?? "")?.[1]
    : undefined;
  return (
    <div className="markdown-code rounded-lg border bg-background/70">
      <div className="flex items-center justify-between gap-3 border-b px-3 py-1 text-[11px] text-muted-foreground">
        <span className="font-mono">{language ?? "text"}</span>
        <CopyText
          text={plainText(children).replace(/\n$/, "")}
          label="Copy code"
          className="h-7 text-[11px]"
        />
      </div>
      <pre>{children}</pre>
    </div>
  );
}

function safeURL(url: string) {
  return /^(https?:\/\/|mailto:|#)/i.test(url) ? url : "";
}

export function Markdown({
  children,
  className,
}: {
  children: string;
  className?: string;
}) {
  return (
    <div className={cn("markdown-body min-w-0", className)}>
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        rehypePlugins={[[rehypeHighlight, { detect: false }]]}
        skipHtml
        urlTransform={(url) =>
          safeURL(url) ||
          (/^(?:\/workspace\/|\.?\.?\/|[\w.-]+\/)/.test(url) ? url : "")
        }
        components={{
          pre: ({ children }) => <CodeBlock>{children}</CodeBlock>,
          table: ({ children }) => (
            <div className="markdown-table">
              <table>{children}</table>
            </div>
          ),
          a: ({ href, children }) =>
            safeURL(href ?? "") ? (
              <a
                href={href}
                target={href?.startsWith("#") ? undefined : "_blank"}
                rel="noopener noreferrer"
              >
                {children}
              </a>
            ) : (
              <span className="markdown-reference" title={href || undefined}>
                {children}
              </span>
            ),
          img: ({ alt }) => (
            <span className="text-muted-foreground">
              [Image{alt ? `: ${alt}` : ""}]
            </span>
          ),
        }}
      >
        {children}
      </ReactMarkdown>
    </div>
  );
}
