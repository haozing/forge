// delivery-search.js — the search island of the public-site HTML face
// (design doc §3): the shell is server-rendered, results arrive from the
// existing JSON face /api/public/sites/{slug}/search. No framework, no
// external requests beyond the same-origin API.
(function () {
  "use strict";
  var results = document.getElementById("search-results");
  if (!results) return;
  var slug = results.getAttribute("data-site-slug") || "";
  var initialQuery = results.getAttribute("data-query") || "";
  var form = document.querySelector(".search-page-form");

  function render(items) {
    results.textContent = "";
    if (!items.length) {
      var empty = document.createElement("p");
      empty.className = "empty";
      empty.textContent = "没有匹配的结果。";
      results.appendChild(empty);
      return;
    }
    items.forEach(function (item) {
      var card = document.createElement("article");
      card.className = "card";
      var title = document.createElement("h3");
      title.className = "card-title";
      var link = document.createElement("a");
      link.href = "/sites/" + slug + "/posts/" + item.display_path;
      link.textContent = item.title || item.display_path;
      title.appendChild(link);
      card.appendChild(title);
      if (item.summary) {
        var summary = document.createElement("p");
        summary.className = "card-summary";
        summary.textContent = item.summary;
        card.appendChild(summary);
      }
      results.appendChild(card);
    });
  }

  function search(query) {
    if (!query) return;
    fetch("/api/public/sites/" + encodeURIComponent(slug) + "/search?q=" + encodeURIComponent(query), {
      headers: { Accept: "application/json" },
    })
      .then(function (response) {
        if (!response.ok) throw new Error("search failed");
        return response.json();
      })
      .then(function (payload) {
        var data = payload && payload.data ? payload.data : payload;
        render((data && data.items) || []);
      })
      .catch(function () {
        results.textContent = "";
        var error = document.createElement("p");
        error.className = "empty";
        error.textContent = "搜索暂时不可用，请稍后重试。";
        results.appendChild(error);
      });
  }

  if (form) {
    form.addEventListener("submit", function (event) {
      event.preventDefault();
      var input = form.querySelector("input[name=q]");
      var query = input ? input.value.trim() : "";
      if (query) search(query);
    });
  }
  if (initialQuery) search(initialQuery);
})();
