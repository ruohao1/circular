import { Link, useRouterState } from "@tanstack/react-router";
import { DocsLayout } from "fumadocs-ui/layouts/docs";
import {
  DocsBody,
  DocsDescription,
  DocsPage,
  DocsTitle,
} from "fumadocs-ui/layouts/docs/page";
import defaultMdxComponents from "fumadocs-ui/mdx";
import { RootProvider } from "fumadocs-ui/provider/tanstack";
import { ArrowLeft } from "lucide-react";
import { lazy, Suspense, use, useEffect } from "react";
import { source } from "@/lib/docs-source";

const DocsSearch = lazy(() => import("@/components/docs-search"));

export default function Documentation() {
  const pathname = useRouterState({
    select: (state) => state.location.pathname,
  });
  const slugs = pathname.slice("/docs".length).split("/").filter(Boolean);
  const page = source.getPage(slugs);

  useEffect(() => {
    document.title = `${page?.data.title ?? "Page not found"} · Circular Docs`;
    return () => {
      document.title = "Circular";
    };
  }, [page]);

  return (
    <RootProvider
      theme={{ enabled: false }}
      search={{ SearchDialog: DocsSearch, preload: false }}
    >
      <a
        href="#docs-content"
        className="sr-only focus:not-sr-only focus:fixed focus:left-4 focus:top-4 focus:z-50 focus:rounded-lg focus:bg-fd-primary focus:p-3 focus:text-fd-primary-foreground"
      >
        Skip to content
      </a>
      <DocsLayout
        tree={source.getPageTree()}
        nav={{
          title: (
            <>
              <span className="grid size-7 place-items-center rounded-lg bg-primary text-sm text-primary-foreground">
                C
              </span>{" "}
              Circular{" "}
              <span className="font-normal text-fd-muted-foreground">Docs</span>
            </>
          ),
          url: "/docs",
        }}
        links={[
          {
            text: "Back to console",
            url: "/",
            icon: <ArrowLeft />,
            active: "none",
          },
        ]}
        themeSwitch={{ enabled: false }}
        sidebar={{ defaultOpenLevel: 1 }}
      >
        <Suspense
          key={pathname}
          fallback={
            <div role="status" className="p-8">
              Loading documentation…
            </div>
          }
        >
          {page ? (
            <Article page={page} />
          ) : (
            <DocsPage
              id="docs-content"
              tableOfContent={{ enabled: false }}
              footer={{ enabled: false }}
            >
              <DocsTitle>Page not found</DocsTitle>
              <DocsDescription>
                This documentation page does not exist.
              </DocsDescription>
              <DocsBody>
                <Link to="/docs">Go to the documentation home</Link>
              </DocsBody>
            </DocsPage>
          )}
        </Suspense>
      </DocsLayout>
    </RootProvider>
  );
}

function Article({
  page,
}: {
  page: NonNullable<ReturnType<typeof source.getPage>>;
}) {
  const { toc } = use(page.data.load());
  const Content = page.data.body;
  const hash = useRouterState({ select: (state) => state.location.hash });
  const hashScrollOptions = useRouterState({
    select: (state) => state.location.state.__hashScrollIntoViewOptions ?? true,
  });

  useEffect(() => {
    // The router can finish navigation while this lazy article is suspended.
    // Honor its section link once the article's headings exist in the DOM.
    if (hash && hashScrollOptions)
      document.getElementById(hash)?.scrollIntoView(hashScrollOptions);
  }, [page, hash, hashScrollOptions]);

  return (
    <DocsPage id="docs-content" toc={toc}>
      <DocsTitle>{page.data.title}</DocsTitle>
      <DocsDescription>{page.data.description}</DocsDescription>
      <DocsBody>
        <Content components={defaultMdxComponents} />
      </DocsBody>
    </DocsPage>
  );
}
