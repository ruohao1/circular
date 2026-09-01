import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import {
  Outlet,
  RouterProvider,
  createRootRoute,
  createRoute,
  createRouter,
} from "@tanstack/react-router";
import * as Separator from "@radix-ui/react-separator";
import React from "react";
import ReactDOM from "react-dom/client";

import "./index.css";

const apiUrl = import.meta.env.VITE_API_URL ?? "http://localhost:8000";

function RootLayout() {
  return (
    <div className="grid min-h-screen grid-cols-[220px_1fr] bg-[#0b0d10] text-zinc-200">
      <aside className="border-r border-white/8 bg-[#0d0f13] p-3">
        <div className="flex h-9 items-center gap-2 px-2 text-sm font-semibold">
          <span className="grid size-5 place-items-center rounded bg-indigo-500 text-[10px] text-white">
            C
          </span>
          Circular
        </div>
        <nav className="mt-5 space-y-1 text-[13px] text-zinc-400">
          <a className="block rounded bg-white/6 px-2 py-1.5 text-zinc-100" href="/">
            Workspace
          </a>
          <span className="block px-2 py-1.5">Runs</span>
          <span className="block px-2 py-1.5">Agents</span>
          <span className="block px-2 py-1.5">Approvals</span>
        </nav>
      </aside>
      <main className="min-w-0"><Outlet /></main>
    </div>
  );
}

function Overview() {
  const health = useQuery({
    queryKey: ["health"],
    queryFn: async () => {
      const response = await fetch(`${apiUrl}/api/v1/health`);
      if (!response.ok) throw new Error("API unavailable");
      return response.json() as Promise<{ status: string }>;
    },
    refetchInterval: 10_000,
  });

  return (
    <section>
      <header className="flex h-12 items-center justify-between border-b border-white/8 px-5">
        <h1 className="text-sm font-medium">Workspace</h1>
        <button className="rounded border border-white/10 bg-white/5 px-2.5 py-1 text-xs text-zinc-300">
          ⌘K &nbsp; Command
        </button>
      </header>
      <div className="mx-auto max-w-5xl p-6">
        <div className="flex items-end justify-between">
          <div>
            <p className="text-xs uppercase tracking-wider text-zinc-500">Control plane</p>
            <h2 className="mt-1 text-xl font-medium">Execution overview</h2>
          </div>
          <span className="flex items-center gap-2 text-xs text-zinc-500">
            <span
              className={`size-1.5 rounded-full ${health.isSuccess ? "bg-emerald-400" : "bg-amber-400"}`}
            />
            {health.isSuccess ? "API connected" : "API connecting"}
          </span>
        </div>
        <Separator.Root className="my-5 h-px bg-white/8" />
        <div className="overflow-hidden rounded-md border border-white/8">
          <div className="grid grid-cols-[1fr_150px_130px] bg-white/[0.025] px-3 py-2 text-[11px] uppercase tracking-wide text-zinc-500">
            <span>Run</span><span>Agent</span><span>Status</span>
          </div>
          <div className="px-3 py-12 text-center text-sm text-zinc-500">
            No runs yet. Create a project, agent, and task through the API.
          </div>
        </div>
      </div>
    </section>
  );
}

const rootRoute = createRootRoute({ component: RootLayout });
const indexRoute = createRoute({ getParentRoute: () => rootRoute, path: "/", component: Overview });
const router = createRouter({ routeTree: rootRoute.addChildren([indexRoute]) });
declare module "@tanstack/react-router" {
  interface Register { router: typeof router }
}

const queryClient = new QueryClient();
ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </React.StrictMode>,
);
