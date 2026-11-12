/** @type {import('tailwindcss').Config} */

// Color roles resolve to CSS variables defined in internal/web/static/input.css.
// Each variable holds space-separated RGB channels so Tailwind opacity
// modifiers (e.g. bg-primary/50) keep working. Light and dark values swap via
// prefers-color-scheme in input.css; templates never use raw hex.
const role = (name) => `rgb(var(--${name}) / <alpha-value>)`;

module.exports = {
  content: [
    "./internal/web/templates/**/*.html",
  ],
  theme: {
    extend: {
      // Window size classes: compact < 600, medium 600-839, expanded >= 840.
      screens: {
        medium: '600px',
        expanded: '840px',
      },
      colors: {
        surface:                 role('surface'),
        'surface-low':           role('surface-low'),
        'surface-cont':          role('surface-cont'),
        'surface-high':          role('surface-high'),
        'on-surface':            role('on-surface'),
        'on-surface-variant':    role('on-surface-variant'),
        outline:                 role('outline'),
        'outline-variant':       role('outline-variant'),
        primary:                 role('primary'),
        'on-primary':            role('on-primary'),
        'primary-cont':          role('primary-cont'),
        'on-primary-cont':       role('on-primary-cont'),
        error:                   role('error'),
        'on-error':              role('on-error'),
        'error-cont':            role('error-cont'),
        'on-error-cont':         role('on-error-cont'),
      },
    },
  },
  plugins: [],
}
