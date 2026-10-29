import { builtinModules } from "node:module";

import js from "@eslint/js";
import reactHooks from "eslint-plugin-react-hooks";
import tseslint from "typescript-eslint";

// Node's built-ins, bare and node:-prefixed: the window and the preload have none.
const nodeBuiltins = builtinModules.flatMap((m) => [m, `node:${m}`]);

export default tseslint.config(
  { ignores: [".vite/", "out/", "src/gen/"] },
  js.configs.recommended,
  ...tseslint.configs.strictTypeChecked,
  {
    languageOptions: {
      parserOptions: {
        project: ["./tsconfig.node.json", "./tsconfig.web.json"],
        tsconfigRootDir: import.meta.dirname,
      },
    },
    rules: {
      // No HTML ever: bodies arrive as spans kithd parsed, and are built as elements.
      "no-restricted-syntax": [
        "error",
        {
          selector: "JSXAttribute[name.name='dangerouslySetInnerHTML']",
          message: "No HTML ever: build React elements from the spans kithd parsed.",
        },
        {
          selector: "MemberExpression[property.name=/^(innerHTML|outerHTML)$/]",
          message: "No HTML ever: build React elements from the spans kithd parsed.",
        },
        {
          selector: "CallExpression[callee.property.name='insertAdjacentHTML']",
          message: "No HTML ever: build React elements from the spans kithd parsed.",
        },
      ],
    },
  },
  {
    files: ["eslint.config.js"],
    ...tseslint.configs.disableTypeChecked,
  },
  // The layers, as depguard keeps Go's: what each process may import.
  {
    files: ["src/renderer/**"],
    ...reactHooks.configs.flat.recommended,
  },
  {
    files: ["src/renderer/**"],
    rules: {
      "no-restricted-imports": [
        "error",
        {
          paths: [
            ...nodeBuiltins.map((name) => ({ name, message: "The window has no Node: it is sandboxed." })),
            { name: "electron", message: "The window reaches main through window.kith alone." },
          ],
          patterns: [
            { group: ["@connectrpc/*"], message: "The window reaches kithd through main (window.kith), never itself." },
            { group: ["**/main/**", "**/preload/**"], message: "The window shares only src/shared and src/gen." },
          ],
        },
      ],
    },
  },
  {
    files: ["src/preload/**"],
    rules: {
      "no-restricted-imports": [
        "error",
        {
          patterns: [
            {
              regex: "^(?!(electron|\\.\\./shared/ipc)$)",
              message: "The preload holds the IPC types and electron, nothing else.",
            },
          ],
        },
      ],
    },
  },
  {
    files: ["src/shared/**"],
    rules: {
      "no-restricted-imports": [
        "error",
        { patterns: [{ group: ["*"], message: "src/shared is types for every layer: it imports nothing." }] },
      ],
    },
  },
  {
    files: ["src/main/**"],
    rules: {
      "no-restricted-imports": [
        "error",
        {
          patterns: [{ group: ["**/renderer/**", "**/preload/**"], message: "Main reaches the window over IPC only." }],
        },
      ],
    },
  },
);
