import globals from "globals";
import js from "@eslint/js";

export default [
  js.configs.recommended,
  {
    files: ["js/**/*.js"],
    languageOptions: {
      ecmaVersion: 2022,
      sourceType: "script",
      globals: {
        ...globals.browser,
        uPlot: "readonly",
      },
    },
    rules: {
      "no-undef": "off",
      "no-unused-vars": ["warn", { vars: "local", args: "none", caughtErrors: "none" }],
      eqeqeq: ["error", "always", { null: "ignore" }],
    },
  },
];
