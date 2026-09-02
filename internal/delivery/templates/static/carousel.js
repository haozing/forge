// delivery-carousel.js — progressive enhancement of the home carousel track
// (二期 §3): the track is fully usable as a scroll-snap list without JS;
// this only adds prev/next buttons and honors reduced-motion.
(function () {
  "use strict";
  var track = document.querySelector(".home--carousel .cards-featured");
  if (!track || window.matchMedia("(prefers-reduced-motion: reduce)").matches) return;
  var controls = document.createElement("div");
  controls.className = "carousel-controls";
  var prev = document.createElement("button");
  prev.type = "button";
  prev.textContent = "←";
  prev.setAttribute("aria-label", "上一张");
  var next = document.createElement("button");
  next.type = "button";
  next.textContent = "→";
  next.setAttribute("aria-label", "下一张");
  controls.appendChild(prev);
  controls.appendChild(next);
  track.parentNode.insertBefore(controls, track);
  function step(direction) {
    var cards = track.querySelectorAll(":scope > .card");
    if (!cards.length) return;
    var width = cards[0].getBoundingClientRect().width + 24;
    track.scrollBy({ left: direction * width, behavior: "smooth" });
  }
  prev.addEventListener("click", function () { step(-1); });
  next.addEventListener("click", function () { step(1); });
})();
