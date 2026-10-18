import * as THREE from 'three';
import { OrbitControls } from 'three/addons/controls/OrbitControls.js';
import { GLTFLoader } from 'three/addons/loaders/GLTFLoader.js';
import { DRACOLoader } from 'three/addons/loaders/DRACOLoader.js';

const mounted = new WeakMap();

function disposeMaterial(material) {
  if (!material) return;
  Object.values(material).forEach(value => {
    if (value && value.isTexture) value.dispose();
  });
  material.dispose?.();
}

function disposeObject(root) {
  root?.traverse(object => {
    if (!object.isMesh) return;
    object.geometry?.dispose();
    const materials = Array.isArray(object.material) ? object.material : [object.material];
    materials.forEach(disposeMaterial);
  });
}

function mount(container, options = {}) {
  mounted.get(container)?.dispose();
  container.innerHTML = '';

  const canvas = document.createElement('canvas');
  canvas.className = 'lb-three-canvas';
  canvas.setAttribute('aria-label', `${options.label || '文物'}三维模型，可拖动旋转、滚轮缩放`);
  canvas.tabIndex = 0;
  container.appendChild(canvas);

  const renderer = new THREE.WebGLRenderer({ canvas, antialias: true, alpha: true });
  renderer.setPixelRatio(Math.min(window.devicePixelRatio || 1, 2));
  renderer.outputColorSpace = THREE.SRGBColorSpace;
  renderer.toneMapping = THREE.ACESFilmicToneMapping;
  renderer.toneMappingExposure = 1.22;

  const scene = new THREE.Scene();
  const camera = new THREE.PerspectiveCamera(36, 1, 0.01, 10000);
  camera.position.set(3, 2.1, 4.8);
  const controls = new OrbitControls(camera, canvas);
  controls.enableDamping = true;
  controls.dampingFactor = 0.07;
  controls.autoRotate = options.autoRotate !== false;
  controls.autoRotateSpeed = 1.15;
  controls.enablePan = true;

  scene.add(new THREE.HemisphereLight(0xfff3d8, 0x14251f, 2.5));
  const key = new THREE.DirectionalLight(0xffd995, 4.1);
  key.position.set(4, 7, 5);
  scene.add(key);
  const fill = new THREE.DirectionalLight(0x8bc8c0, 1.5);
  fill.position.set(-5, 2, -4);
  scene.add(fill);
  const rim = new THREE.DirectionalLight(0xffe6a5, 1.25);
  rim.position.set(1, 1, -5);
  scene.add(rim);

  const root = new THREE.Group();
  scene.add(root);
  let model = null;
  let stopped = false;
  let frame = 0;

  function resize() {
    const width = Math.max(1, container.clientWidth);
    const height = Math.max(1, container.clientHeight);
    renderer.setSize(width, height, false);
    camera.aspect = width / height;
    camera.updateProjectionMatrix();
  }

  function resetView() {
    if (!model) return;
    const bounds = new THREE.Box3().setFromObject(model);
    const size = bounds.getSize(new THREE.Vector3());
    const center = bounds.getCenter(new THREE.Vector3());
    const largest = Math.max(size.x, size.y, size.z, 0.001);
    const distance = (largest * 0.5) / Math.tan(THREE.MathUtils.degToRad(camera.fov * 0.5)) * 1.72;
    controls.target.copy(center);
    camera.position.copy(center).add(new THREE.Vector3(0.62, 0.35, 1).normalize().multiplyScalar(distance));
    camera.near = Math.max(largest / 1000, 0.001);
    camera.far = Math.max(largest * 100, 100);
    controls.minDistance = largest * 0.16;
    controls.maxDistance = largest * 10;
    camera.updateProjectionMatrix();
    controls.update();
  }

  const observer = new ResizeObserver(resize);
  observer.observe(container);
  resize();

  const draco = new DRACOLoader();
  draco.setDecoderPath('./vendor/draco/');
  const loader = new GLTFLoader();
  loader.setDRACOLoader(draco);
  options.onStatus?.('loading');
  loader.load(options.modelUrl, gltf => {
    if (stopped) {
      disposeObject(gltf.scene);
      return;
    }
    model = gltf.scene;
    model.traverse(object => {
      if (!object.isMesh) return;
      object.castShadow = false;
      const materials = Array.isArray(object.material) ? object.material : [object.material];
      materials.forEach(material => {
        if (!material) return;
        material.needsUpdate = true;
        if ('envMapIntensity' in material) material.envMapIntensity = 1.15;
      });
    });
    root.add(model);
    resetView();
    options.onStatus?.('ready');
  }, event => {
    if (event.total) options.onProgress?.(Math.round(event.loaded / event.total * 100));
  }, error => {
    console.error('Relic GLB load failed:', error);
    options.onStatus?.('error', error);
  });

  function render() {
    if (stopped) return;
    controls.update();
    renderer.render(scene, camera);
    frame = requestAnimationFrame(render);
  }
  render();

  const handle = {
    reset: resetView,
    setAutoRotate(value) { controls.autoRotate = !!value; },
    dispose() {
      if (stopped) return;
      stopped = true;
      cancelAnimationFrame(frame);
      observer.disconnect();
      controls.dispose();
      draco.dispose();
      disposeObject(model);
      renderer.dispose();
      container.innerHTML = '';
      mounted.delete(container);
    }
  };
  mounted.set(container, handle);
  return handle;
}

window.ShujiRelic3D = {
  mount,
  dispose(container) { mounted.get(container)?.dispose(); }
};

