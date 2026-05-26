/**
 * ViewRouter — 轻量级视图路由器
 * 
 * 每个视图通过 ViewRouter.register(id, viewDef) 注册，
 * 路由切换时调用对应 view 的 render(container) 方法。
 * 
 * 设计特点：
 * - 兼容 file:// 协议（不依赖 ES Modules / fetch）
 * - 全局注册表模式
 * - 支持 init / destroy 生命周期钩子
 */
var ViewRouter = (function() {
  'use strict';

  var views = {};        // id -> { render, init?, destroy? }
  var currentView = null;
  var container = null;
  var onNavigateCallbacks = [];

  /**
   * 注册一个视图
   * @param {string} id - 视图ID，对应 data-page 属性值
   * @param {object} viewDef - { render(container), init?(), destroy?() }
   */
  function register(id, viewDef) {
    if (!viewDef || typeof viewDef.render !== 'function') {
      console.error('[ViewRouter] View "' + id + '" must have a render(container) method');
      return;
    }
    views[id] = viewDef;
  }

  /**
   * 导航到指定视图
   * @param {string} id - 目标视图ID
   * @param {object} [params] - 可选参数传递给 render
   */
  function navigate(id, params) {
    if (!views[id]) {
      console.warn('[ViewRouter] View "' + id + '" not registered');
      return;
    }

    // 销毁当前视图
    if (currentView && views[currentView] && typeof views[currentView].destroy === 'function') {
      views[currentView].destroy();
    }

    currentView = id;

    // 获取容器
    var c = getContainer();
    if (!c) {
      console.error('[ViewRouter] Container element not found');
      return;
    }

    // 渲染新视图
    views[id].render(c, params);

    // 调用 init 钩子
    if (typeof views[id].init === 'function') {
      views[id].init(c, params);
    }

    // 触发导航回调
    for (var i = 0; i < onNavigateCallbacks.length; i++) {
      onNavigateCallbacks[i](id, params);
    }
  }

  /**
   * 获取页面容器 DOM 元素
   */
  function getContainer() {
    if (!container) {
      container = document.getElementById('page-container');
    }
    return container;
  }

  /**
   * 获取当前活跃的视图 ID
   */
  function getCurrentView() {
    return currentView;
  }

  /**
   * 注册导航事件回调
   * @param {function} callback - function(viewId, params)
   */
  function onNavigate(callback) {
    if (typeof callback === 'function') {
      onNavigateCallbacks.push(callback);
    }
  }

  /**
   * 获取所有已注册的视图 ID 列表
   */
  function getRegisteredViews() {
    return Object.keys(views);
  }

  // Public API
  return {
    register: register,
    navigate: navigate,
    getContainer: getContainer,
    getCurrentView: getCurrentView,
    onNavigate: onNavigate,
    getRegisteredViews: getRegisteredViews
  };
})();
