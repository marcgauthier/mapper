// Use the existing native change handlers; scoped Select2 refreshes must not
// trigger another map load while dependent options are being rebuilt.
const locationFilterControls = (() => {
  const jq = window.jQuery;
  const labels = {siteFilter: 'sites', buildingFilter: 'buildings', roomFilter: 'telecom rooms', networkFilter: 'networks'};
  function refresh() {
    for (const id of Object.keys(labels)) {
      const select = document.getElementById(id), control = jq(select);
      if (!control.data('select2')) {
        control.select2({
          width: '190px', minimumResultsForSearch: 0, allowClear: true,
          placeholder: {id: '', text: 'All ' + labels[id]}
        });
        control.on('change.mapperLocations', () => changeLocationFilter({siteFilter:'site', buildingFilter:'building', roomFilter:'room', networkFilter:'network'}[id]));
        control.on('select2:open', () => {
          const search = control.data('select2').dropdown.$search;
          search.attr('aria-label', 'Search ' + labels[id]);
          search.attr('placeholder', 'Search ' + labels[id] + '…');
          search.trigger('focus');
        });
      }
      if (select.disabled) control.select2('close');
      control.trigger('change.select2');
      control.data('select2').$container.attr('title', select.title || 'Filter by ' + labels[id]);
    }
  }
  return {refresh};
})();
