// The modal's Access picker. Ticked Access rules sit on top in the order
// they apply, and changing Exposure swaps the old defaults for the new ones
// while keeping the user's own picks.
document.addEventListener("change", (e) => {
  const picker = e.target.form?.querySelector("[data-access]");
  if (!picker) return;
  const items = [...picker.querySelectorAll("label")];
  const box = (item) => item.querySelector("input");
  if (e.target.name === "exposure") {
    const defaults = JSON.parse(picker.dataset.defaults);
    const was = defaults[picker.dataset.exposure] || [], now = defaults[e.target.value] || [];
    picker.dataset.exposure = e.target.value;
    for (const item of items) {
      const name = box(item).value;
      if (now.includes(name)) box(item).checked = true;
      else if (was.includes(name) && !item.dataset.picked) box(item).checked = false;
      item.querySelector("[data-default]").hidden = !now.includes(name);
    }
    // Defaults first in their configured order, then other picks, then the rest.
    const rank = (item) => {
      const i = now.indexOf(box(item).value);
      return i >= 0 ? i : box(item).checked ? now.length : now.length + 1;
    };
    picker.append(...items.sort((a, b) => rank(a) - rank(b)));
  } else if (e.target.name === "access") {
    const item = e.target.closest("label");
    item.dataset.picked = e.target.checked ? "yes" : ""; // a user's own pick survives swaps
    if (!e.target.checked) return;
    const last = items.filter((i) => i !== item && box(i).checked).pop();
    last ? last.after(item) : picker.prepend(item);
  }
});
