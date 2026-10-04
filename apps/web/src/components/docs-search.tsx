import { useDocsSearch } from "fumadocs-core/search/client";
import { createFromSource } from "fumadocs-core/search/server";
import {
  SearchDialog,
  SearchDialogClose,
  SearchDialogContent,
  SearchDialogHeader,
  SearchDialogIcon,
  SearchDialogInput,
  SearchDialogList,
  SearchDialogOverlay,
  type SharedProps,
} from "fumadocs-ui/components/dialog/search";
import { source } from "@/lib/docs-source";

// The index is built locally on first search. Documentation needs no API or account.
const search = createFromSource(source);

export default function DocsSearch(props: SharedProps) {
  const result = useDocsSearch({ client: search });
  return (
    <SearchDialog
      {...props}
      search={result.search}
      onSearchChange={result.setSearch}
      isLoading={result.query.isLoading}
    >
      <SearchDialogOverlay />
      <SearchDialogContent>
        <SearchDialogHeader>
          <SearchDialogIcon />
          <SearchDialogInput />
          <SearchDialogClose />
        </SearchDialogHeader>
        {result.query.error ? (
          <p role="alert" className="p-4 text-sm text-fd-muted-foreground">
            Search could not load. Try again or browse the sidebar.
          </p>
        ) : (
          <SearchDialogList
            items={result.query.data !== "empty" ? result.query.data : null}
          />
        )}
      </SearchDialogContent>
    </SearchDialog>
  );
}
