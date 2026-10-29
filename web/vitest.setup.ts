// happy-dom does not implement window.visualViewport, which Vuetify's overlay
// location strategy consults when anchoring dialogs. A minimal stub keeps
// component tests focused on behaviour instead of viewport plumbing.
if (!window.visualViewport) {
  Object.defineProperty(window, 'visualViewport', {
    configurable: true,
    value: {
      addEventListener: () => {},
      removeEventListener: () => {},
      width: 1280,
      height: 720,
      offsetTop: 0,
      offsetLeft: 0,
      pageTop: 0,
      pageLeft: 0,
      scale: 1,
    },
  })
}
