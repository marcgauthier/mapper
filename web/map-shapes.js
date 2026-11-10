// Sample a cloud outline once; the map polygon and editor SVG share its geometry.
const cloudShape = (() => {
  const points = [], add = (x, y) => points.push(x / 100 - 1, y / 60 - 1);
  let from = [40, 112];
  add(...from);
  for (const [a, b, to] of [
    [[18, 112], [2, 96], [2, 77]],
    [[2, 59], [16, 44], [34, 44]],
    [[30, 24], [46, 8], [65, 8]],
    [[78, 8], [87, 14], [94, 24]],
    [[102, 8], [118, 2], [133, 6]],
    [[153, 10], [166, 26], [164, 44]],
    [[184, 44], [198, 58], [198, 77]],
    [[198, 96], [183, 112], [164, 112]]
  ]) {
    for (let step = 1; step <= 12; step++) {
      const t = step / 12, u = 1 - t;
      add(u*u*u*from[0] + 3*u*u*t*a[0] + 3*u*t*t*b[0] + t*t*t*to[0], u*u*u*from[1] + 3*u*u*t*a[1] + 3*u*t*t*b[1] + t*t*t*to[1]);
    }
    from = to;
  }
  const svgPoints = [];
  for (let i = 0; i < points.length; i += 2) svgPoints.push(((points[i] + 1) * 100) + ',' + ((points[i+1] + 1) * 60));
  return {points: points.join(' '), svgPoints: svgPoints.join(' ')};
})();
