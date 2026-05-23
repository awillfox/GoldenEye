/** @type {import('tailwindcss').Config} */
module.exports = {
  content: [
    "./internal/web/templates/**/*.html",
  ],
  theme: {
    extend: {
      fontFamily: {
        display: ['"Plus Jakarta Sans"', 'ui-sans-serif', 'system-ui', 'sans-serif'],
        body:    ['"DM Sans"', 'ui-sans-serif', 'system-ui', 'sans-serif'],
      },
      colors: {
        neu: {
          base:           '#E0E5EC',
          fg:             '#3D4852',
          muted:          '#6B7280',
          accent:         '#6C63FF',
          'accent-light': '#8B84FF',
          success:        '#38B2AC',
        },
      },
      boxShadow: {
        'neu':             '9px 9px 16px rgba(163,177,198,0.6), -9px -9px 16px rgba(255,255,255,0.5)',
        'neu-hover':       '12px 12px 20px rgba(163,177,198,0.7), -12px -12px 20px rgba(255,255,255,0.6)',
        'neu-sm':          '5px 5px 10px rgba(163,177,198,0.6), -5px -5px 10px rgba(255,255,255,0.5)',
        'neu-inset':       'inset 6px 6px 10px rgba(163,177,198,0.6), inset -6px -6px 10px rgba(255,255,255,0.5)',
        'neu-inset-deep':  'inset 10px 10px 20px rgba(163,177,198,0.7), inset -10px -10px 20px rgba(255,255,255,0.6)',
        'neu-inset-sm':    'inset 3px 3px 6px rgba(163,177,198,0.6), inset -3px -3px 6px rgba(255,255,255,0.5)',
        'neu-accent':      '5px 5px 10px rgba(108,99,255,0.4), -5px -5px 10px rgba(255,255,255,0.5)',
        'neu-accent-hover':'8px 8px 14px rgba(108,99,255,0.5), -8px -8px 14px rgba(255,255,255,0.6)',
        'neu-accent-press':'inset 4px 4px 8px rgba(0,0,0,0.2), inset -2px -2px 4px rgba(255,255,255,0.1)',
        'neu-modal':       '16px 16px 32px rgba(163,177,198,0.8), -8px -8px 20px rgba(255,255,255,0.9)',
        'neu-nav':         '0 6px 12px rgba(163,177,198,0.5), 0 -2px 6px rgba(255,255,255,0.8)',
      },
      keyframes: {
        float: {
          '0%, 100%': { transform: 'translateY(0px)' },
          '50%':      { transform: 'translateY(-8px)' },
        },
      },
      animation: {
        float: 'float 3s ease-in-out infinite',
      },
    },
  },
  plugins: [],
}
