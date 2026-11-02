/// <reference types="vite/client" />

// Makes side-effect imports like `import "./styles/global.css"` typecheck
// under tsconfig's noUncheckedSideEffectImports flag: vite/client declares
// the `*.css` module shape that a plain .css file does not.
