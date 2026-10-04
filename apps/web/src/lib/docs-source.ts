import { loader } from "fumadocs-core/source";
import { defineDocs } from "fumadocs-mdx/macro";

// Only user guides are published and indexed. Contributor references stay in the repo.
const docs = defineDocs({
  dir: "../../docs/user-guide",
  docs: { async: true },
});

export const source = loader({
  baseUrl: "/docs",
  source: docs.toFumadocsSource(),
});
