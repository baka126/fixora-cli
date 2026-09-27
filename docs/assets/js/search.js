(function () {
  var input = document.getElementById('site-search');
  var list = document.getElementById('page-list');
  if (!input || !list) return;
  var links = Array.prototype.slice.call(list.querySelectorAll('a[data-search]'));
  input.addEventListener('input', function () {
    var query = input.value.trim().toLowerCase();
    links.forEach(function (link) {
      link.hidden = query !== '' && link.getAttribute('data-search').indexOf(query) === -1;
    });
  });
}());
